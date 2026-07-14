package connections

import (
	"net/http"
	"sort"
	"strings"

	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	sqlinspect "github.com/flanksource/commons-db/inspect/sql"
	"github.com/flanksource/commons-db/models"
)

type browserCatalog struct {
	Nodes []browserCatalogNode `json:"nodes"`
}

type browserCatalogNode struct {
	ID       string               `json:"id"`
	Label    string               `json:"label"`
	Kind     string               `json:"kind"`
	Query    string               `json:"query,omitempty"`
	Options  map[string]any       `json:"options,omitempty"`
	Children []browserCatalogNode `json:"children,omitempty"`
}

func (h *connectionBrowserHandler) serveCatalog(w http.ResponseWriter, r *http.Request, conn *models.Connection) {
	switch conn.Type {
	case models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeClickHouse:
		catalog, err := h.sqlCatalog(r, conn)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		writeJSON(w, catalog)
	case models.ConnectionTypeOpenSearch:
		catalog, err := h.openSearchCatalog(r, conn)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		writeJSON(w, catalog)
	default:
		writeJSON(w, browserCatalog{Nodes: []browserCatalogNode{}})
	}
}

func (h *connectionBrowserHandler) sqlCatalog(r *http.Request, conn *models.Connection) (browserCatalog, error) {
	inspected, err := h.inspectSQL(r.Context(), conn, "")
	if err != nil {
		return browserCatalog{}, err
	}
	return browserCatalog{Nodes: catalogNodesForSQL(conn.Type, inspected)}, nil
}

func catalogNodesForSQL(connType string, inspected sqlinspect.Catalog) []browserCatalogNode {
	type relationCatalog struct {
		kind     string
		children []browserCatalogNode
	}
	groups := map[string]map[string]relationCatalog{}
	for _, schema := range inspected.Schemas {
		groups[schema.Name] = map[string]relationCatalog{}
		for _, relation := range schema.Relations {
			kind := relation.Type
			if kind != "view" {
				kind = "table"
			}
			entry := relationCatalog{kind: kind}
			for _, column := range relation.Columns {
				entry.children = append(entry.children, browserCatalogNode{
					ID: schema.Name + "." + relation.Name + "." + column.Name, Label: column.Name + " · " + column.DataType, Kind: "column",
				})
			}
			groups[schema.Name][relation.Name] = entry
		}
	}
	schemas := make([]string, 0, len(groups))
	for name := range groups {
		schemas = append(schemas, name)
	}
	sort.Strings(schemas)
	var nodes []browserCatalogNode
	for _, schemaName := range schemas {
		tables := make([]string, 0, len(groups[schemaName]))
		for name := range groups[schemaName] {
			tables = append(tables, name)
		}
		sort.Strings(tables)
		schemaNode := browserCatalogNode{ID: schemaName, Label: schemaName, Kind: "schema"}
		for _, tableName := range tables {
			relation := groups[schemaName][tableName]
			identifier := sqlIdentifier(connType, schemaName, tableName)
			queryText := "SELECT * FROM " + identifier + " LIMIT 100"
			if connType == models.ConnectionTypeSQLServer {
				queryText = "SELECT TOP 100 * FROM " + identifier
			}
			schemaNode.Children = append(schemaNode.Children, browserCatalogNode{
				ID: schemaName + "." + tableName, Label: tableName, Kind: relation.kind, Query: queryText,
				Children: relation.children,
			})
		}
		nodes = append(nodes, schemaNode)
	}
	return nodes
}

func sqlIdentifier(connType, schema, table string) string {
	if connType == models.ConnectionTypeMySQL || connType == models.ConnectionTypeClickHouse {
		return "`" + strings.ReplaceAll(schema, "`", "``") + "`.`" + strings.ReplaceAll(table, "`", "``") + "`"
	}
	if connType == models.ConnectionTypeSQLServer {
		return "[" + strings.ReplaceAll(schema, "]", "]]") + "].[" + strings.ReplaceAll(table, "]", "]]") + "]"
	}
	return `"` + strings.ReplaceAll(schema, `"`, `""`) + `"."` + strings.ReplaceAll(table, `"`, `""`) + `"`
}

func (h *connectionBrowserHandler) openSearchCatalog(r *http.Request, conn *models.Connection) (browserCatalog, error) {
	inspection, err := h.inspectConnection(r.Context(), conn, "", "", "")
	if err != nil {
		return browserCatalog{}, err
	}
	return browserCatalog{Nodes: inspection.Nodes}, nil
}

func catalogNodesForOpenSearch(targets []opensearchinspect.Target) []browserCatalogNode {
	nodes := make([]browserCatalogNode, 0, len(targets))
	for _, target := range targets {
		nodes = append(nodes, browserCatalogNode{
			ID: target.Kind + ":" + target.Name, Label: target.Name, Kind: target.Kind, Query: `{"query":{"match_all":{}}}`,
			Options: map[string]any{"index": target.Name, "targetKind": target.Kind, "limit": "200"},
		})
	}
	return nodes
}
