package providers

import (
	stdcontext "context"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/flanksource/commons-db/query"
	"github.com/google/uuid"
)

const clickHouseDiagnosticLogLimit = 100

type clickHouseDiagnosticCapture struct {
	mu sync.Mutex

	queryID   string
	progress  clickhouse.Progress
	profile   *clickhouse.ProfileInfo
	logs      []map[string]any
	logBytes  int
	truncated bool
}

func clickHouseDiagnosticContext(
	parent stdcontext.Context,
	enabled bool,
) (stdcontext.Context, string, func() map[string]any) {
	if !enabled {
		return parent, "", func() map[string]any { return nil }
	}
	capture := &clickHouseDiagnosticCapture{queryID: uuid.NewString()}
	ctx := clickhouse.Context(
		parent,
		clickhouse.WithQueryID(capture.queryID),
		clickhouse.WithSettings(clickhouse.Settings{"send_logs_level": "debug"}),
		clickhouse.WithProgress(capture.addProgress),
		clickhouse.WithProfileInfo(capture.setProfile),
		clickhouse.WithLogs(capture.addLog),
	)
	return ctx, capture.queryID, capture.snapshot
}

func (c *clickHouseDiagnosticCapture) addProgress(progress *clickhouse.Progress) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.progress.Rows += progress.Rows
	c.progress.Bytes += progress.Bytes
	c.progress.TotalRows += progress.TotalRows
	c.progress.WroteRows += progress.WroteRows
	c.progress.WroteBytes += progress.WroteBytes
	c.progress.Elapsed += progress.Elapsed
}

func (c *clickHouseDiagnosticCapture) setProfile(profile *clickhouse.ProfileInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := *profile
	c.profile = &copy
}

func (c *clickHouseDiagnosticCapture) addLog(log *clickhouse.Log) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.logs) >= clickHouseDiagnosticLogLimit || c.logBytes+len(log.Text) > query.DiagnosticPreviewLimit {
		c.truncated = true
		return
	}
	c.logBytes += len(log.Text)
	c.logs = append(c.logs, map[string]any{
		"time": log.Time.Format(time.RFC3339Nano), "hostname": log.Hostname,
		"queryId": log.QueryID, "threadId": log.ThreadID, "priority": log.Priority,
		"source": log.Source, "text": log.Text,
	})
}

func (c *clickHouseDiagnosticCapture) snapshot() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	details := map[string]any{
		"queryId": c.queryID,
		"progress": map[string]any{
			"rows": c.progress.Rows, "bytes": c.progress.Bytes,
			"totalRows": c.progress.TotalRows, "wroteRows": c.progress.WroteRows,
			"wroteBytes": c.progress.WroteBytes, "elapsedMs": float64(c.progress.Elapsed) / float64(time.Millisecond),
		},
	}
	if c.profile != nil {
		details["profile"] = map[string]any{
			"rows": c.profile.Rows, "bytes": c.profile.Bytes, "blocks": c.profile.Blocks,
			"appliedLimit": c.profile.AppliedLimit, "rowsBeforeLimit": c.profile.RowsBeforeLimit,
			"calculatedRowsBeforeLimit": c.profile.CalculatedRowsBeforeLimit,
		}
	}
	if len(c.logs) > 0 {
		details["logs"] = append([]map[string]any(nil), c.logs...)
	}
	if c.truncated {
		details["logsTruncated"] = true
	}
	return details
}
