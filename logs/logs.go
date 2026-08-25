package logs

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query/datetime"
	"github.com/flanksource/commons/tokenizer"
)

type LogLine struct {
	ID            string            `json:"id,omitempty"`
	FirstObserved time.Time         `json:"firstObserved,omitempty"`
	LastObserved  *time.Time        `json:"lastObserved,omitempty"`
	Count         int               `json:"count,omitempty"`
	Message       string            `json:"message"`
	Hash          string            `json:"hash,omitempty"`
	Severity      string            `json:"severity,omitempty"`
	Source        string            `json:"source,omitempty"`
	Host          string            `json:"host,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

func (t *LogLine) SetHash() {
	t.Hash = tokenizer.Tokenize(t.Message)
}

func (t LogLine) GetFieldKey(fields []string, messageFields ...string) string {
	if len(fields) == 0 {
		return ""
	}

	values := make([]string, len(fields))
	for i, field := range fields {
		values[i] = t.GetFieldValue(field, messageFields...)
	}

	return strings.Join(values, "\u0000")
}

var DefaultMessageFields = []string{"msg", "message"}

// EffectiveMessage is the line's message, falling back to the label a shipper
// may have carried it in when the body itself came through empty.
func (t LogLine) EffectiveMessage(messageFields ...string) string {
	if t.Message != "" {
		return t.Message
	}
	if t.Labels == nil {
		return ""
	}
	if len(messageFields) == 0 {
		messageFields = DefaultMessageFields
	}
	for _, field := range messageFields {
		if msg := t.Labels[field]; msg != "" {
			return msg
		}
	}
	return ""
}

func (t LogLine) GetFieldValue(field string, messageFields ...string) string {
	switch field {
	case "message":
		return fmt.Sprintf("msg::%s", t.EffectiveMessage(messageFields...))
	case "hash":
		return fmt.Sprintf("hash::%s", t.Hash)
	case "severity":
		return fmt.Sprintf("severity::%s", t.Severity)
	case "source":
		return fmt.Sprintf("source::%s", t.Source)
	case "host":
		return fmt.Sprintf("host::%s", t.Host)
	case "firstObserved":
		return fmt.Sprintf("firstObserved::%d", t.FirstObserved.UnixNano())
	case "lastObserved":
		if t.LastObserved == nil {
			return "lastObserved::unknown"
		}
		return fmt.Sprintf("lastObserved::%d", t.LastObserved.UnixNano())
	case "count":
		return fmt.Sprintf("count::%d", t.Count)
	case "id":
		return fmt.Sprintf("id::%s", t.ID)
	default:
		// A bare name and a "label."-prefixed one address the same label, so
		// normalize before the lookup — otherwise a bare field name matches
		// nothing and every line keys identically.
		labelKey := strings.TrimPrefix(field, "label.")

		if t.Labels == nil {
			return fmt.Sprintf("label.%s=unknown", labelKey)
		}

		return fmt.Sprintf("label.%s=%s", labelKey, t.Labels[labelKey])
	}
}

func (t *LogLine) TemplateContext(messageFields ...string) map[string]any {
	return map[string]any{
		"id":            t.ID,
		"firstObserved": t.FirstObserved,
		"lastObserved":  t.LastObserved,
		"count":         t.Count,
		"message":       t.EffectiveMessage(messageFields...),
		"hash":          t.Hash,
		"severity":      t.Severity,
		"source":        t.Source,
		"host":          t.Host,
		"labels":        t.Labels,
	}
}

type LogResult struct {
	Metadata map[string]any `json:"metadata,omitempty"`
	Logs     []*LogLine     `json:"logs,omitempty"`
}

type LogsRequestBase struct {
	// The start time for the query
	// SupportsDatemath
	Start string `json:"start,omitempty"`

	// The end time for the query
	// Supports Datemath
	End string `json:"end,omitempty"`

	// Limit is the maximum number of lines to return
	Limit string `json:"limit,omitempty" template:"true"`
}

// GetStart resolves the lower edge of the read window.
//
// It goes through datetime.Parse rather than go-datemath directly because the
// two grammars are not the same: a bound written as date math ("now-1h") is
// go-datemath's, but a bound a profile parameter already resolved is an
// RFC3339Nano instant, and go-datemath's fraction stops at milliseconds — it
// rejects the very timestamps this package is handed.
func (r *LogsRequestBase) GetStart() (time.Time, error) {
	parsed, err := datetime.Parse(r.Start, time.Now())
	if err != nil {
		return time.Time{}, err
	}
	return parsed.Time, nil
}

func (r *LogsRequestBase) GetEnd() (time.Time, error) {
	parsed, err := datetime.Parse(r.End, time.Now())
	if err != nil {
		return time.Time{}, err
	}
	return parsed.Time, nil
}
