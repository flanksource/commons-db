package observability

import (
	"errors"
	"fmt"
	"time"

	dbgorm "github.com/flanksource/commons-db/gorm"
	"github.com/flanksource/commons/har"
)

const previewSQL = "SELECT * FROM orders WHERE tenant_id = ?"

func runtimeJSONExample(definition EventDefinition, family ProviderFamily, provider string) map[string]any {
	duration, rows := previewMetrics(definition.Event, family)
	values := map[string]any{
		"event": string(definition.Event), "connection_level": definition.Default,
		"provider": provider, "connection": previewConnection(family),
		"duration_ms": duration.Milliseconds(), "rows": rows,
	}
	switch definition.Event {
	case EventError:
		values["error"] = previewError(family)
		addPreviewRequest(values, family, true)
	case EventSlow:
		values["slow_threshold_ms"] = int64(1000)
		addPreviewRequest(values, family, false)
	case EventOperation:
		values["sql"] = "SCAN cache"
	case EventSQL:
		values["sql"] = previewSQL
	case EventSQLParams:
		values["params"] = []any{"t****"}
	case EventHTTP, EventHTTPHeaders, EventHTTPRequestBody, EventHTTPResponseBody:
		addHTTPExample(values, definition.Event)
	}
	return values
}

func addPreviewRequest(values map[string]any, family ProviderFamily, failed bool) {
	switch family {
	case ProviderSQL:
		values["sql"] = previewSQL
	case ProviderHTTP:
		values["method"] = "POST"
		values["url"] = previewHTTPEntry().Request.URL
		if failed {
			values["status"] = 503
		} else {
			values["status"] = 200
		}
	default:
		values["sql"] = "SCAN cache"
	}
}

func addHTTPExample(values map[string]any, event Event) {
	entry := previewHTTPEntry()
	values["method"] = entry.Request.Method
	values["url"] = entry.Request.URL
	values["status"] = entry.Response.Status
	switch event {
	case EventHTTPHeaders:
		values["request_headers"] = entry.Request.Headers
		values["query"] = entry.Request.QueryString
		values["response_headers"] = entry.Response.Headers
	case EventHTTPRequestBody:
		values["request_body"] = entry.Request.PostData.Text
		values["body_size"] = entry.Request.BodySize
	case EventHTTPResponseBody:
		values["response_body"] = entry.Response.Content.Text
		values["body_size"] = entry.Response.BodySize
		values["truncated"] = entry.Response.Content.Truncated
	}
}

func runtimePrettyExample(definition EventDefinition, family ProviderFamily, provider string) string {
	identity := fmt.Sprintf("[%s/%s] ", provider, previewConnection(family))
	duration, rows := previewMetrics(definition.Event, family)
	if family == ProviderSQL {
		return identity + previewSQLTrace(definition.Event, duration, rows)
	}
	if family == ProviderHTTP {
		if detail := previewHTTPDetail(definition.Event); detail != "" {
			return identity + detail
		}
	}
	return identity + previewCompletion(definition.Event, family, duration, rows)
}

func previewSQLTrace(event Event, duration time.Duration, rows int64) string {
	if event == EventSQLParams {
		return "params [t****]"
	}
	trace := dbgorm.SQLTrace{Duration: duration, Rows: rows, SQL: previewSQL}
	if event == EventError {
		trace.Err = errors.New(previewError(ProviderSQL))
	}
	trace.Slow = event == EventSlow
	return trace.Pretty().String()
}

func previewHTTPDetail(event Event) string {
	entry := previewHTTPEntry()
	var options har.DetailOptions
	switch event {
	case EventHTTP:
		return entry.Pretty().String()
	case EventHTTPHeaders:
		options = har.DetailOptions{RequestHeaders: true, QueryString: true, ResponseHeaders: true}
	case EventHTTPRequestBody:
		options = har.DetailOptions{RequestBody: true}
	case EventHTTPResponseBody:
		options = har.DetailOptions{ResponseBody: true}
	default:
		return ""
	}
	return entry.Pretty().String() + "\n" + entry.Detail(options).String()
}

func previewCompletion(event Event, family ProviderFamily, duration time.Duration, rows int64) string {
	prefix := ""
	if event == EventError {
		prefix = "ERROR >="
	} else if event == EventSlow {
		prefix = "SLOW >= "
	}
	text := fmt.Sprintf("%s[%dms] [rows:%d]", prefix, duration.Milliseconds(), rows)
	if family == ProviderHTTP {
		status := 200
		if event == EventError {
			status = 503
		}
		text += fmt.Sprintf(" POST %s [%d]", previewHTTPEntry().Request.URL, status)
	} else {
		text += " SCAN cache"
	}
	if event == EventError {
		text += " " + previewError(family)
	}
	return text
}

func previewMetrics(event Event, family ProviderFamily) (time.Duration, int64) {
	switch event {
	case EventError:
		return 82 * time.Millisecond, 0
	case EventSlow:
		if family == ProviderHTTP {
			return 1200 * time.Millisecond, 1
		}
		return 1200 * time.Millisecond, 25
	case EventOperation:
		return 18 * time.Millisecond, 12
	case EventSQL, EventSQLParams:
		return 48 * time.Millisecond, 25
	default:
		return 86 * time.Millisecond, 1
	}
}

func previewConnection(family ProviderFamily) string {
	switch family {
	case ProviderSQL:
		return "warehouse"
	case ProviderHTTP:
		return "search"
	default:
		return "cache"
	}
}

func previewError(family ProviderFamily) string {
	switch family {
	case ProviderSQL:
		return "syntax error"
	case ProviderHTTP:
		return "request timed out"
	default:
		return "operation failed"
	}
}

func previewHTTPEntry() har.Entry {
	return har.Entry{
		Time: 86,
		Request: har.Request{
			Method: "POST", URL: "https://api.example.test/_search", BodySize: 68,
			Headers:     []har.Header{{Name: "Authorization", Value: "Bearer ********"}, {Name: "Content-Type", Value: "application/json"}},
			QueryString: []har.QueryString{{Name: "namespace", Value: "tenant-x"}},
			PostData:    &har.PostData{MimeType: "application/json", Text: `{"query":{"match_all":{}},"token":"********"}`},
		},
		Response: har.Response{
			Status: 200, StatusText: "OK", BodySize: 512,
			Headers: []har.Header{{Name: "Content-Type", Value: "application/json"}},
			Content: har.Content{Size: 512, MimeType: "application/json", Text: `{"hits":["..."]}`, Truncated: true},
		},
	}
}
