package deadlocks

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// CatalogEntry is one hobt of the connected database's current catalog.
type CatalogEntry struct {
	HobtID    int64
	Object    string
	Index     string
	IndexType string
}

const catalogQuery = `SELECT p.hobt_id, OBJECT_NAME(p.object_id) AS object_name, ISNULL(i.name, '') AS index_name, i.type_desc
FROM sys.partitions AS p
JOIN sys.indexes AS i ON i.object_id = p.object_id AND i.index_id = p.index_id
WHERE p.hobt_id IN (%s)`

// Resolve names the index every index lock sits on, and its type, from the
// connected database's catalog. A page lock carries only a hobt id, and
// classification needs to know which index is the clustered one, so graphs
// are resolved before they are analysed.
//
// The catalog is today's: a hobt id replaced by a rebuild since the deadlock
// no longer resolves, and a lock in another database is not looked up.
func Resolve(ctx context.Context, db *sql.DB, graphs []Graph) error {
	var current int
	if err := db.QueryRowContext(ctx, "SELECT DB_ID()").Scan(&current); err != nil {
		return fmt.Errorf("read the connected database id: %w", err)
	}
	var hobts []any
	seen := map[int64]bool{}
	for _, graph := range graphs {
		for _, resource := range graph.Resources {
			if resource.HobtID != 0 && resource.DatabaseID == current && !seen[resource.HobtID] {
				seen[resource.HobtID] = true
				hobts = append(hobts, resource.HobtID)
			}
		}
	}
	var entries []CatalogEntry
	if len(hobts) > 0 {
		var err error
		if entries, err = readCatalog(ctx, db, hobts); err != nil {
			return fmt.Errorf("resolve %d hobt ids against sys.partitions: %w", len(hobts), err)
		}
	}
	ApplyCatalog(graphs, current, entries)
	return nil
}

// readCatalog reads the catalog rows of hobts, one bind marker per id.
func readCatalog(ctx context.Context, db *sql.DB, hobts []any) ([]CatalogEntry, error) {
	markers := make([]string, len(hobts))
	for i := range hobts {
		markers[i] = fmt.Sprintf("@p%d", i+1)
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(catalogQuery, strings.Join(markers, ", ")), hobts...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var entries []CatalogEntry
	for rows.Next() {
		// OBJECT_NAME is NULL for an object dropped since the read began; the
		// index and its type still describe the lock.
		var entry CatalogEntry
		var object sql.NullString
		if err := rows.Scan(&entry.HobtID, &object, &entry.Index, &entry.IndexType); err != nil {
			return nil, err
		}
		entry.Object = object.String
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// ApplyCatalog fills each index lock's object, index and index type from
// entries, the connected database's (currentDatabaseID) catalog rows.
func ApplyCatalog(graphs []Graph, currentDatabaseID int, entries []CatalogEntry) {
	byHobt := make(map[int64]CatalogEntry, len(entries))
	for _, entry := range entries {
		byHobt[entry.HobtID] = entry
	}
	for g := range graphs {
		for r := range graphs[g].Resources {
			resource := &graphs[g].Resources[r]
			if resource.HobtID == 0 {
				continue
			}
			named := resource.Index != ""
			entry, found := byHobt[resource.HobtID]
			switch {
			case resource.DatabaseID != currentDatabaseID:
				if !named {
					resource.Resolution = ResolutionOtherDatabase
				}
			case found:
				resource.IndexType = IndexType(entry.IndexType)
				if !named {
					resource.Object, resource.Index = entry.Object, entry.Index
					resource.Resolution = ResolutionResolved
				}
			case !named:
				resource.Resolution = ResolutionRebuiltSince
			}
		}
	}
}
