package connections

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/clicky/entity"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	"github.com/flanksource/commons-db/models"
)

var indexTargetGroups = map[string]string{"group": "Patterns", "index": "Indexes", "alias": "Aliases", "data_stream": "Data streams"}

// serveIndexLookup serves the `index` lookup of an OpenSearch connection form:
// the saved connection named by `id` is inspected (through the same cache the
// browser uses) and its targets offered by indexOptions.
func (s *Service) serveIndexLookup(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	id := params.Get("id")
	if id == "" {
		entity.WriteError(w, entity.NewStatusErrorf(http.StatusUnprocessableEntity, "unsaved_connection", "save the connection to look up its indexes; any index or wildcard pattern can still be typed"))
		return
	}
	conn, err := s.browser.hydratedConnection(id)
	if err != nil {
		entity.WriteError(w, entity.NewStatusErrorf(http.StatusNotFound, "not_found", "%s", err.Error()))
		return
	}
	if conn.Type != models.ConnectionTypeOpenSearch && conn.Type != models.ConnectionTypeElasticSearch {
		entity.WriteError(w, entity.NewStatusErrorf(http.StatusBadRequest, "invalid_filter", "connection type %q has no indexes to look up", conn.Type))
		return
	}
	ctx, cancel := inspectionContext(r.Context(), conn.Type, 15*time.Second)
	defer cancel()
	inspection, err := s.browser.inspectConnection(ctx, conn, "", "", "", false)
	if err != nil {
		entity.WriteError(w, entity.NewStatusErrorf(http.StatusUnprocessableEntity, "inspection_failed", "inspect %s: %s", conn.Name, sanitizeConnectionError(err, conn)))
		return
	}
	options, total := indexOptions(inspection.Targets, params.Get("__lookup_q"), 100)
	response := entity.LookupResponse{Filters: map[string]entity.LookupFilter{"index": {Label: "Index", Options: options, Total: total, Truncated: total > len(options)}}}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		panic(err)
	}
}

// indexOptions builds the index lookup from inspected OpenSearch targets.
// Without a query it offers the wildcard groups and the targets that are not
// rotations of one; a query matches every target by substring, or — when it
// holds a `*` — as an OpenSearch wildcard, offering the typed pattern itself
// with how many indexes it covers. Hidden and system targets are left out, as
// an OpenSearch wildcard leaves them out. limit caps the options; total counts
// every match.
func indexOptions(targets []opensearchinspect.Target, query string, limit int) (map[string]entity.LookupNode, int) {
	query = strings.TrimSpace(query)
	glob := strings.Contains(query, "*")
	var matcher *regexp.Regexp
	if glob {
		matcher = regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(query), `\*`, ".*") + "$")
	}
	var candidates []entity.LookupNode
	covered := 0
	for _, target := range targets {
		group, known := indexTargetGroups[target.Kind]
		if !known || target.Hidden || target.System {
			continue
		}
		var matched bool
		switch {
		case glob:
			matched = matcher.MatchString(target.Name)
			if matched && target.Kind == "index" {
				covered++
			}
		case query != "":
			matched = strings.Contains(strings.ToLower(target.Name), strings.ToLower(query))
		default:
			matched = target.Kind == "group" || target.Pattern == ""
		}
		if !matched {
			continue
		}
		node := entity.LookupNode{Kind: "text", Plain: target.Name, Text: target.Name, Group: group}
		if target.Kind == "group" {
			node.Description = fmt.Sprintf("%d indexes", target.Count)
		}
		candidates = append(candidates, node)
	}
	sort.SliceStable(candidates, func(a, b int) bool { return candidates[a].Plain < candidates[b].Plain })
	if glob {
		description := fmt.Sprintf("matches %d indexes", covered)
		if covered == 0 {
			description = "matches no indexes"
		}
		candidates = append([]entity.LookupNode{{Kind: "text", Plain: query, Text: query, Group: "Pattern", Description: description}}, candidates...)
	}
	options := make(map[string]entity.LookupNode, min(len(candidates), max(limit, 0)))
	for _, node := range candidates {
		if limit > 0 && len(options) >= limit {
			break
		}
		options[node.Plain] = node
	}
	return options, len(candidates)
}
