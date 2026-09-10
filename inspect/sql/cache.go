package sqlinspect

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	inspection "github.com/flanksource/commons-db/inspect"
)

// Cache owns catalogue storage and fill coordination. Implementations must honor
// GetOptions.Refresh and inspection.WithRefresh, coalesce concurrent fills, and
// return cache metadata, including stale values alongside refresh errors. Load
// runs locally; a remote implementation stores the Catalog, not the callback.
// *inspection.Memo[Catalog] implements this interface.
type Cache interface {
	Get(context.Context, inspection.GetOptions[Catalog]) (inspection.Result[Catalog], error)
}

// Options identifies the source independently of transient SQL pool handles.
type Options struct {
	// CacheKey must isolate the connection, authorization/context scope, selected
	// database, and connection revision. Change it when credentials or connection
	// settings change. Use an opaque identity, never credentials or a raw DSN.
	// Driver and effective limits are appended by Inspect.
	CacheKey string
	// Cache replaces the default process-lifetime memory cache; it is not layered
	// with it. A shared backend needs CacheKey values stable across processes.
	Cache   Cache
	Refresh bool
	// Open is used instead of db for short-lived connections. It is called only
	// on a fill, with the fill context, and Inspect closes the returned pool.
	Open func(context.Context) (*sql.DB, error)
}

var defaultCache = NewMemoryCache()

// NewMemoryCache creates an isolated cache with the SQL freshness and capacity
// policy. Reuse it across inspections; nil Options.Cache uses the shared default.
// Like all Memos, it registers with inspection.Stats and inspection.Flush.
func NewMemoryCache() *inspection.Memo[Catalog] {
	return inspection.NewMemo(inspection.MemoOptions[Catalog]{
		Policy: inspection.Policy(inspection.CacheClassSQLCatalog),
		Weight: catalogWeight,
	})
}

// Inspect returns a cached catalogue of the current database, with freshness
// metadata. Pass either a caller-owned db or Options.Open and identify its
// connection, authorization context, revision, and selected database in CacheKey.
// Keep a caller-owned db open while background refreshes may use it. Returned
// slices and nested pointers are shared cache values and must be treated as read-only.
// A failed explicit refresh returns the last known catalogue alongside its error.
func Inspect(ctx context.Context, db *sql.DB, driver string, limits Limits, options Options) (Catalog, error) {
	if (db == nil) == (options.Open == nil) {
		return Catalog{}, fmt.Errorf("sql inspection requires either a database or an opener")
	}
	baseKey := strings.TrimSpace(options.CacheKey)
	if baseKey == "" {
		return Catalog{}, fmt.Errorf("sql inspection cache key is required")
	}
	driver = normalizeDriver(driver)
	if _, _, err := inspectionQueries(driver); err != nil {
		return Catalog{}, err
	}
	limits = limits.withDefaults()
	key := fmt.Sprintf("%s:sql-catalog:v1:%s:%d:%d:%d:%d", baseKey, driver,
		limits.MaxRelations, limits.MaxColumns, limits.MaxRoutines, limits.MaxDefinitionBytes)
	cache := options.Cache
	if cache == nil {
		cache = defaultCache
	}
	result, err := cache.Get(ctx, inspection.GetOptions[Catalog]{
		Key: key, Refresh: options.Refresh || inspection.RefreshRequested(ctx),
		Load: func(loadContext context.Context) (Catalog, error) {
			client := db
			if options.Open != nil {
				var err error
				client, err = options.Open(loadContext)
				if err != nil {
					return Catalog{}, err
				}
				if client == nil {
					return Catalog{}, fmt.Errorf("sql inspection opener returned a nil database")
				}
				defer client.Close()
			}
			return inspect(loadContext, client, driver, limits)
		},
	})
	result.Value.Cache = &result.Cache
	return result.Value, err
}

func catalogWeight(catalog Catalog) int {
	weight := len(catalog.Databases) + len(catalog.Schemas)
	for _, schema := range catalog.Schemas {
		weight += len(schema.Relations) + len(schema.Routines)
		for _, routine := range schema.Routines {
			weight += len(routine.Parameters) + len(routine.SQL)/1024
		}
		for _, relation := range schema.Relations {
			weight += len(relation.Columns) + len(relation.Indexes) + len(relation.ForeignKeys) + len(relation.Triggers) + len(relation.ViewDef)/1024
			for _, trigger := range relation.Triggers {
				weight += len(trigger.SQL) / 1024
			}
		}
	}
	return weight
}
