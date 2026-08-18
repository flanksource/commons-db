package query

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
	dbgorm "github.com/flanksource/commons-db/gorm"
	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons/har"
	"github.com/flanksource/commons/logger"
)

func (operation *connectionOperation) log(level logger.LogLevel, event observability.Event, values map[string]any, pretty api.Textable) {
	if logger.IsJSONLogger(operation.ctx.Logger) {
		operation.ctx.Logger.V(level).
			WithValues(structuredLogValues(level, event, values)...).
			Infof("connection")
		return
	}
	operation.ctx.Logger.V(level).Infof("%s", pretty.ANSI())
}

func structuredLogValues(level logger.LogLevel, event observability.Event, values map[string]any) []any {
	fields := make(map[string]any, len(values)+2)
	for key, value := range values {
		fields[key] = value
	}
	fields["event"] = string(event)
	fields["connection_level"] = level.String()

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	attributes := make([]any, 0, len(fields)*2)
	for _, key := range keys {
		attributes = append(attributes, key, fields[key])
	}
	return attributes
}

func (operation *connectionOperation) prettySummary(event observability.Event, duration time.Duration, rows int, runErr error) api.Textable {
	snapshot := operation.diagnostics.Snapshot()
	if operation.policy.Family == observability.ProviderSQL {
		statement := ""
		if snapshot != nil {
			statement = logger.StripSecrets(snapshot.Request.Query)
		}
		var sanitizedErr error
		if runErr != nil {
			sanitizedErr = errors.New(logger.StripSecrets(runErr.Error()))
		}
		return operation.withIdentity(dbgorm.SQLTrace{
			Duration: duration,
			Rows:     int64(rows),
			SQL:      statement,
			Slow:     event == observability.EventSlow,
			Err:      sanitizedErr,
		}.Pretty())
	}
	if operation.policy.Family == observability.ProviderHTTP && event == observability.EventHTTP && operation.collector != nil {
		return operation.withIdentity(operation.collector.Pretty())
	}

	text := api.Text{}
	switch event {
	case observability.EventError:
		text = text.AddText("ERROR >=", "font-bold text-red-500")
	case observability.EventSlow:
		text = text.AddText("SLOW >= ", "font-bold text-yellow-500")
	}
	text = text.
		AddText(fmt.Sprintf("[%dms] ", duration.Milliseconds()), "text-yellow-500").
		AddText(fmt.Sprintf("[rows:%d]", rows), "font-bold text-blue-500")
	if snapshot != nil && snapshot.Request.URL != "" {
		text = text.AddText(fmt.Sprintf(" %s %s [%d]", snapshot.Request.Method, snapshot.Request.URL, snapshot.Response.Status))
	} else if snapshot != nil && snapshot.Request.Query != "" {
		text = text.AddText(" " + logger.StripSecrets(snapshot.Request.Query))
	}
	if snapshot != nil && len(snapshot.Request.Details) > 0 {
		text = text.Space().AddText("filters="+formatLogDetails(snapshot.Request.Details), "text-muted")
	}
	if runErr != nil {
		text = text.AddText(" " + logger.StripSecrets(runErr.Error()))
	}
	return operation.withIdentity(text)
}

func (operation *connectionOperation) prettyParams(params []any) api.Textable {
	return operation.withIdentity(api.Text{}.
		AddText("params ", "font-bold text-muted").
		AddText(oneLineLogValue(params)))
}

func formatLogDetails(details map[string]any) string {
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	formatted := make([]string, 0, len(keys))
	for _, key := range keys {
		formatted = append(formatted, key+": "+oneLineLogValue(details[key]))
	}
	return strings.Join(formatted, ", ")
}

func oneLineLogValue(value any) string {
	return strings.Join(strings.Fields(fmt.Sprint(value)), " ")
}

func (operation *connectionOperation) prettyHTTPEntry(entry har.Entry, options har.DetailOptions) api.Textable {
	text := operation.withIdentity(entry.Pretty())
	if detail := entry.Detail(options); detail != nil {
		text = text.NewLine().Add(detail)
	}
	return text
}

func (operation *connectionOperation) withIdentity(content api.Textable) api.Text {
	return api.Text{}.
		AddText(fmt.Sprintf("[%s/%s] ", operation.provider, operation.connection), "text-muted").
		Add(content)
}
