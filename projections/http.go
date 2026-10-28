package projections

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	chttp "github.com/flanksource/commons/http"
	"github.com/ohler55/ojg/jp"
)

func parseProjectionURLTemplate(raw string) (*template.Template, error) {
	return template.New("url").Option("missingkey=error").Parse(raw)
}

func renderProjectionURL(compiled *template.Template, entry map[string]any) (string, error) {
	var rendered strings.Builder
	if err := compiled.Execute(&rendered, entry); err != nil {
		return "", err
	}
	return rendered.String(), nil
}

func projectionEntries(p Projection, values map[string]any) ([]map[string]any, error) {
	if p.Spec.Target == nil {
		return nil, fmt.Errorf("per-target source requires spec.target")
	}
	body, err := os.ReadFile(p.resolvePath(p.Spec.Target.Path))
	if err != nil {
		return nil, err
	}
	_, _, entries, err := projectionTarget(body, p.Spec.Target.Select)
	if err != nil {
		return nil, err
	}
	compiled, err := compileProjection(p)
	if err != nil {
		return nil, err
	}
	selected := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		eligible, err := projectionTargetEligible(compiled, entry, values)
		if err != nil {
			return nil, fmt.Errorf("spec.target.filter: %w", err)
		}
		if eligible {
			selected = append(selected, entry)
		}
	}
	return selected, nil
}

func queryHTTPProjection(ctx context.Context, p Projection, values map[string]any) ([]map[string]any, []ProjectionWarning, error) {
	query := *p.Spec.Source.Query.HTTP
	compiled, err := parseProjectionURLTemplate(query.URL)
	if err != nil {
		return nil, nil, err
	}
	entries, err := projectionEntries(p, values)
	if err != nil {
		return nil, nil, err
	}
	urls := make([]string, len(entries))
	for i, entry := range entries {
		urls[i], err = renderProjectionURL(compiled, entry)
		if err != nil {
			return nil, nil, fmt.Errorf("http URL template: %w", err)
		}
	}
	// Hydrate credentials through the shared connection provider, retaining the per-target URL.
	if _, err := query.HTTPConnection.Hydrate(ctx, ctx.GetNamespace()); err != nil {
		return nil, nil, err
	}
	client, err := connection.CreateHTTPClient(ctx, query.HTTPConnection)
	if err != nil {
		return nil, nil, err
	}
	client.Timeout(30 * time.Second)
	method := query.Method
	if method == "" {
		method = http.MethodGet
	}
	items := make([]map[string]any, 0, len(entries))
	var warnings []ProjectionWarning
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, warnings, err
		}
		payload, warning := fetchProjectionRow(ctx, client, method, urls[i], query)
		if warning != nil {
			warnings = append(warnings, *warning)
			continue
		}
		row := projectionTargetRow(entry, payload)
		row["url"] = urls[i]
		items = append(items, row)
	}
	return items, warnings, ctx.Err()
}

func projectionTargetRow(entry map[string]any, payload any) map[string]any {
	row := map[string]any{}
	for key, value := range entry {
		switch value.(type) {
		case string, bool, int, int64, float64, nil:
			row[key] = value
		}
	}
	row["body"] = payload
	return row
}

func fetchProjectionRow(ctx context.Context, client *chttp.Client, method, url string, query ProjectionHTTPQuery) (any, *ProjectionWarning) {
	attempts, wait := query.Accepted.Attempts, query.Accepted.Wait
	if attempts == 0 {
		attempts = 5
	}
	if wait == 0 {
		wait = 2 * time.Second
	}
	warning := func(err error) (any, *ProjectionWarning) {
		return nil, &ProjectionWarning{Source: url, Message: err.Error(), Count: 1}
	}
	for attempt := 1; ; attempt++ {
		response, err := client.R(ctx).Do(method, url)
		if err != nil {
			return warning(err)
		}
		if response.StatusCode == http.StatusAccepted {
			response.Body.Close()
			if attempt >= attempts {
				return warning(fmt.Errorf("still computing after %d attempts (HTTP 202)", attempts))
			}
			select {
			case <-ctx.Done():
				return warning(ctx.Err())
			case <-time.After(wait):
			}
			continue
		}
		if !response.IsOK() {
			response.Body.Close()
			return warning(fmt.Errorf("%s", response.Status))
		}
		var payload any
		decoder := json.NewDecoder(response.Body)
		err = decoder.Decode(&payload)
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				err = fmt.Errorf("response must contain exactly one JSON value")
			}
		}
		response.Body.Close()
		if err != nil {
			return warning(fmt.Errorf("decoding response: %w", err))
		}
		payload, err = selectPayload(payload, query.Select)
		if err != nil {
			return warning(err)
		}
		return payload, nil
	}
}

func selectPayload(payload any, selector string) (any, error) {
	if selector == "" {
		return payload, nil
	}
	path, err := jp.ParseString(selector)
	if err != nil {
		return nil, err
	}
	selected := path.Get(payload)
	if len(selected) != 1 {
		return nil, fmt.Errorf("select matched %d values, expected exactly 1", len(selected))
	}
	return selected[0], nil
}

func projectionMap(value any) (map[string]any, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var mapped map[string]any
	if err := json.Unmarshal(body, &mapped); err != nil {
		return nil, err
	}
	return mapped, nil
}
