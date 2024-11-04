package upstream

import (
	gocontext "context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons/properties"
	"github.com/flanksource/duty/api"
	"github.com/flanksource/duty/context"
	dutil "github.com/flanksource/duty/db"
	"github.com/flanksource/duty/models"
	"github.com/samber/lo"
	"github.com/samber/oops"
	"github.com/sethvargo/go-retry"
	"gorm.io/gorm"
)

type pushableTable interface {
	models.DBTable
	GetUnpushed(db *gorm.DB) ([]models.DBTable, error)
}

type customIsPushedUpdater interface {
	UpdateIsPushed(db *gorm.DB, items []models.DBTable) error
}

type parentIsPushedUpdater interface {
	UpdateParentsIsPushed(ctx *gorm.DB, items []models.DBTable) error
}

// Compile time check to ensure that tables with parent implement this interface.
var (
	_ parentIsPushedUpdater = (*models.ConfigItem)(nil)
	_ parentIsPushedUpdater = (*models.ConfigChange)(nil)
	_ parentIsPushedUpdater = (*models.ConfigChange)(nil)
	_ parentIsPushedUpdater = (*models.ConfigAnalysis)(nil)
	_ parentIsPushedUpdater = (*models.ConfigRelationship)(nil)

	_ parentIsPushedUpdater = (*models.Component)(nil)
	_ parentIsPushedUpdater = (*models.ComponentRelationship)(nil)
	_ parentIsPushedUpdater = (*models.ConfigComponentRelationship)(nil)

	_ parentIsPushedUpdater = (*models.Check)(nil)
	_ parentIsPushedUpdater = (*models.CheckStatus)(nil)
)

type ForeignKeyErrorSummary struct {
	Count int      `json:"count,omitempty"`
	IDs   []string `json:"ids,omitempty"`
}

const FKErrorIDCount = 10

func (fks ForeignKeyErrorSummary) MarshalJSON() ([]byte, error) {
	// Display less IDs to keep UI consistent
	idLimit := properties.Int(FKErrorIDCount, "upstream.summary.fkerror_id_count")
	fks.IDs = lo.Slice(fks.IDs, 0, idLimit)
	if len(fks.IDs) >= idLimit {
		fks.IDs = append(fks.IDs, "...")
	}
	return json.Marshal(map[string]any{"ids": fks.IDs, "count": fks.Count})
}

type ReconcileTableSummary struct {
	Success   int                    `json:"success,omitempty"`
	FKeyError ForeignKeyErrorSummary `json:"foreign_error,omitempty"`
	Skipped   bool                   `json:"skipped,omitempty"`
	Error     *oops.OopsError        `json:"error,omitempty"`
}

type ReconcileSummary map[string]ReconcileTableSummary

// DidReconcile returns true if all of the given tables
// reconciled successfully.
func (t ReconcileSummary) DidReconcile(tables []string) bool {
	if len(tables) == 0 {
		return true
	}

	if t == nil {
		return false // nothing has been reconciled yet
	}

	for _, table := range tables {
		summary, ok := t[table]
		if !ok {
			return false // this table hasn't been reconciled yet
		}

		reconciled := !summary.Skipped && summary.Error == nil && summary.FKeyError.Count == 0
		if !reconciled {
			return false // table didn't reconcile successfully
		}
	}

	return true
}

func (t ReconcileSummary) GetSuccessFailure() (int, int) {
	var success, failure int
	for _, summary := range t {
		success += summary.Success
		failure += summary.FKeyError.Count
	}
	return success, failure
}

func (t *ReconcileSummary) AddSkipped(tables ...pushableTable) {
	if t == nil || (*t) == nil {
		(*t) = make(ReconcileSummary)
	}

	for _, table := range tables {
		v := (*t)[table.TableName()]
		v.Skipped = true
		(*t)[table.TableName()] = v
	}
}

func (t *ReconcileSummary) AddStat(table string, success int, foreignKeyFailures ForeignKeyErrorSummary, err error) {
	if success == 0 && foreignKeyFailures.Count == 0 && err == nil {
		return
	}

	if t == nil || (*t) == nil {
		(*t) = make(ReconcileSummary)
	}

	v := (*t)[table]
	v.Success = success
	v.FKeyError = foreignKeyFailures
	if err != nil {
		// For json marshaling
		v.Error = lo.ToPtr(oops.Wrap(err).(oops.OopsError))
	}

	(*t)[table] = v
}

func (t ReconcileSummary) Error() error {
	var allErrors []string
	for table, summary := range t {
		if summary.Error != nil {
			allErrors = append(allErrors, fmt.Sprintf("%s: %s; ", table, summary.Error))
		}

		if summary.FKeyError.Count > 0 {
			allErrors = append(allErrors, fmt.Sprintf("%s: %d foreign key errors; ", table, summary.Error))
		}
	}

	if len(allErrors) == 0 {
		return nil
	}

	return errors.New(strings.Join(allErrors, ";"))
}

// PushGroup are a set of tables that need to be reconciled in order.
// If one fails, the rest are skipped.
type PushGroup struct {
	Name   string
	Tables []pushableTable

	// DependsOn is a list of tables that need to be reconciled
	// for this group to be reconciled.
	DependsOn []string
}

var reconcileTableGroups = []PushGroup{
	{
		Name:   "configs",
		Tables: []pushableTable{models.ConfigScraper{}, models.ConfigItem{}, models.ConfigChange{}, models.ConfigAnalysis{}, models.ConfigRelationship{}},
	},
	{
		Name:   "topologies",
		Tables: []pushableTable{models.Topology{}, models.Component{}, models.ComponentRelationship{}},
	},
	{
		Name:   "canaries",
		Tables: []pushableTable{models.Canary{}, models.Check{}, models.CheckStatus{}},
	},
	{
		Name:      "CheckComponentRelationship",
		Tables:    []pushableTable{models.CheckComponentRelationship{}},
		DependsOn: []string{models.Check{}.TableName(), models.Component{}.TableName()},
	},
	{
		Name:      "CheckConfigRelationship",
		Tables:    []pushableTable{models.CheckConfigRelationship{}},
		DependsOn: []string{models.Check{}.TableName(), models.ConfigItem{}.TableName()},
	},
	{
		Name:      "ConfigComponentRelationship",
		Tables:    []pushableTable{models.ConfigComponentRelationship{}},
		DependsOn: []string{models.ConfigItem{}.TableName(), models.Component{}.TableName()},
	},
	{
		Name:   "JobHistory",
		Tables: []pushableTable{models.JobHistory{}},
	},
	{
		Name:   "Artifact",
		Tables: []pushableTable{models.Artifact{}},
	},
}

func ReconcileAll(ctx context.Context, config UpstreamConfig, batchSize int) ReconcileSummary {
	return ReconcileSome(ctx, config, batchSize)
}

func ReconcileSome(ctx context.Context, config UpstreamConfig, batchSize int, runOnly ...string) ReconcileSummary {
	var summary ReconcileSummary

	for _, group := range reconcileTableGroups {
		if !summary.DidReconcile(group.DependsOn) {
			summary.AddSkipped(group.Tables...)
			continue
		}

	outer:
		for i, table := range group.Tables {
			if len(runOnly) > 0 && !lo.Contains(runOnly, table.TableName()) {
				continue
			}

			success, failed, err := reconcileTable(ctx, config, table, batchSize)
			summary.AddStat(table.TableName(), success, failed, err)
			if err != nil {
				if i != len(group.Tables)-1 {
					// If there are remaining tables in this group, skip them.
					summary.AddSkipped(group.Tables[i+1:]...)
				}

				break outer
			}
		}
	}

	return summary
}

// ReconcileTable pushes all unpushed items in a table to upstream.
func reconcileTable(ctx context.Context, config UpstreamConfig, table pushableTable, batchSize int) (int, ForeignKeyErrorSummary, error) {
	client := NewUpstreamClient(config)

	var count int
	var fkFailed ForeignKeyErrorSummary
	for {
		items, err := table.GetUnpushed(ctx.DB().Limit(batchSize))
		if err != nil {
			return count, fkFailed, fmt.Errorf("failed to fetch unpushed items for table %s: %w", table, err)
		}

		if len(items) == 0 {
			return count, fkFailed, nil
		}

		var fkErrorOccured bool

		ctx.Tracef("pushing %s %d to upstream", table.TableName(), len(items))
		pushError := client.Push(ctx, NewPushData(items))
		if pushError != nil {
			httpError := api.HTTPErrorFromErr(pushError)
			if httpError == nil || httpError.Data == "" {
				return count, fkFailed, fmt.Errorf("failed to push %s to upstream: %w", table.TableName(), pushError)
			}

			var foreignKeyErr PushFKError
			if err := json.Unmarshal([]byte(httpError.Data), &foreignKeyErr); err != nil {
				return count, fkFailed, fmt.Errorf("failed to push %s to upstream (could not decode api error: %w): %w", table.TableName(), err, pushError)
			}

			fkErrorOccured = !foreignKeyErr.Empty()

			failedOnes := lo.SliceToMap(foreignKeyErr.IDs, func(item string) (string, struct{}) {
				return item, struct{}{}
			})
			failedItems := lo.Filter(items, func(item models.DBTable, _ int) bool {
				_, ok := failedOnes[item.PK()]
				if ok {
					fkFailed.IDs = append(fkFailed.IDs, item.PK())
					fkFailed.Count += 1
				}
				return ok
			})

			if c, ok := table.(parentIsPushedUpdater); ok && len(failedItems) > 0 {
				if err := c.UpdateParentsIsPushed(ctx.DB(), failedItems); err != nil {
					return count, fkFailed, fmt.Errorf("failed to mark parents as unpushed: %w", err)
				}
			}

			items = lo.Filter(items, func(item models.DBTable, _ int) bool {
				_, ok := failedOnes[item.PK()]
				return !ok
			})
		}

		count += len(items)

		batchSize := ctx.Properties().Int("update_is_pushed.batch.size", 200)
		for _, batch := range lo.Chunk(items, batchSize) {
			backoff := retry.WithJitter(time.Second, retry.WithMaxRetries(3, retry.NewExponential(time.Second)))
			err = retry.Do(ctx, backoff, func(_ctx gocontext.Context) error {
				ctx = _ctx.(context.Context)

				if c, ok := table.(customIsPushedUpdater); ok {
					if err := c.UpdateIsPushed(ctx.DB(), batch); err != nil {
						if dutil.IsDeadlockError(err) {
							return retry.RetryableError(err)
						}

						return fmt.Errorf("failed to update is_pushed on %s: %w", table.TableName(), err)
					}
				} else {
					ids := lo.Map(batch, func(a models.DBTable, _ int) string { return a.PK() })
					if err := ctx.DB().Model(table).Where("id IN ?", ids).Update("is_pushed", true).Error; err != nil {
						if dutil.IsDeadlockError(err) {
							return retry.RetryableError(err)
						}

						return fmt.Errorf("failed to update is_pushed on %s: %w", table.TableName(), err)
					}
				}

				return nil
			})
			if err != nil {
				return count, fkFailed, err
			}
		}

		if fkErrorOccured {
			// we stop reconciling for this table.
			// In the next reconciliation run, the parents will be pushed
			// and the fk error will resolve then.
			return count, fkFailed, nil
		}

		if pushError != nil {
			return count, fkFailed, pushError
		}
	}
}
