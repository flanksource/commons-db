package opensearch

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	openSearchErrorBodyLimit = 64 << 10
	openSearchReasonLimit    = 2048
)

type OpenSearchErrorCause struct {
	Type   string
	Reason string
}

// OpenSearchError is a bounded summary of a non-success OpenSearch response.
type OpenSearchError struct {
	Operation     string
	StatusCode    int
	Status        string
	Type          string
	Reason        string
	Causes        []OpenSearchErrorCause
	BodyTruncated bool
}

func (e *OpenSearchError) Error() string {
	message := fmt.Sprintf("opensearch: %s failed with status %s", e.Operation, e.Status)
	if e.Type != "" || e.Reason != "" {
		message += ": " + formatOpenSearchCause(OpenSearchErrorCause{Type: e.Type, Reason: e.Reason})
	}
	for _, cause := range e.Causes {
		message += "; caused by " + formatOpenSearchCause(cause)
	}
	if e.BodyTruncated {
		message += " [response truncated]"
	}
	return message
}

type openSearchErrorCausePayload struct {
	Type     string                       `json:"type"`
	Reason   string                       `json:"reason"`
	CausedBy *openSearchErrorCausePayload `json:"caused_by"`
}

type openSearchErrorEnvelope struct {
	Error struct {
		Type         string                        `json:"type"`
		Reason       string                        `json:"reason"`
		RootCauses   []openSearchErrorCausePayload `json:"root_cause"`
		CausedBy     *openSearchErrorCausePayload  `json:"caused_by"`
		FailedShards []struct {
			Reason openSearchErrorCausePayload `json:"reason"`
		} `json:"failed_shards"`
	} `json:"error"`
	Status int `json:"status"`
}

func readOpenSearchError(operation string, statusCode int, status string, body io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(body, openSearchErrorBodyLimit+1))
	if err != nil {
		return fmt.Errorf("read OpenSearch %s error response: %w", operation, err)
	}
	truncated := len(data) > openSearchErrorBodyLimit
	if truncated {
		data = data[:openSearchErrorBodyLimit]
	}
	parsed := &OpenSearchError{
		Operation: operation, StatusCode: statusCode, Status: status, BodyTruncated: truncated,
	}
	var envelope openSearchErrorEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		parsed.Type = "invalid_error_response"
		parsed.Reason = truncateOpenSearchReason(strings.TrimSpace(string(data)))
		return parsed
	}
	if envelope.Status != 0 {
		parsed.StatusCode = envelope.Status
	}
	primary := primaryOpenSearchCause(envelope)
	parsed.Type = primary.Type
	parsed.Reason = truncateOpenSearchReason(primary.Reason)
	seen := map[string]bool{primary.Type + "\x00" + primary.Reason: true}
	for cause := primary.CausedBy; cause != nil; cause = cause.CausedBy {
		key := cause.Type + "\x00" + cause.Reason
		if seen[key] {
			continue
		}
		seen[key] = true
		parsed.Causes = append(parsed.Causes, OpenSearchErrorCause{
			Type: cause.Type, Reason: truncateOpenSearchReason(cause.Reason),
		})
	}
	return parsed
}

func primaryOpenSearchCause(envelope openSearchErrorEnvelope) openSearchErrorCausePayload {
	if len(envelope.Error.RootCauses) > 0 {
		primary := envelope.Error.RootCauses[0]
		for _, shard := range envelope.Error.FailedShards {
			if shard.Reason.Type == primary.Type && shard.Reason.Reason == primary.Reason {
				return shard.Reason
			}
		}
		return primary
	}
	if envelope.Error.CausedBy != nil {
		return *envelope.Error.CausedBy
	}
	if len(envelope.Error.FailedShards) > 0 {
		return envelope.Error.FailedShards[0].Reason
	}
	return openSearchErrorCausePayload{Type: envelope.Error.Type, Reason: envelope.Error.Reason}
}

func formatOpenSearchCause(cause OpenSearchErrorCause) string {
	if cause.Type == "" {
		return cause.Reason
	}
	if cause.Reason == "" {
		return cause.Type
	}
	return cause.Type + ": " + cause.Reason
}

func truncateOpenSearchReason(reason string) string {
	runes := []rune(reason)
	if len(runes) <= openSearchReasonLimit {
		return reason
	}
	return string(runes[:openSearchReasonLimit]) + "... [truncated]"
}
