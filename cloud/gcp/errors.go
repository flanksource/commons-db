package gcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// APIError is a Google API error response, parsed from the JSON error envelope
// {"error": {"code": ..., "message": ..., "status": ..., "errors": [{"reason": ...}]}}.
type APIError struct {
	StatusCode int
	Code       int
	Status     string
	Message    string
	Reason     string
}

func (e *APIError) Error() string {
	parts := []string{fmt.Sprintf("gcp: HTTP %d", e.StatusCode)}
	if e.Status != "" {
		parts = append(parts, e.Status)
	}
	if e.Reason != "" {
		parts = append(parts, "("+e.Reason+")")
	}
	text := strings.Join(parts, " ")
	if e.Message != "" {
		text += ": " + e.Message
	}
	return text
}

// Is lets a 404 match fs.ErrNotExist so filesystem callers need no GCP types.
func (e *APIError) Is(target error) bool {
	return target == fs.ErrNotExist && e.StatusCode == http.StatusNotFound
}

type errorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

// parseAPIError builds an APIError from a non-2xx response body. A body that is
// not the JSON envelope keeps its text as the message, so nothing is lost.
func parseAPIError(statusCode int, body io.Reader) *APIError {
	raw, _ := io.ReadAll(body)
	apiErr := &APIError{StatusCode: statusCode}
	var envelope errorEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || (envelope.Error.Message == "" && envelope.Error.Code == 0) {
		apiErr.Message = strings.TrimSpace(string(raw))
		return apiErr
	}
	apiErr.Code = envelope.Error.Code
	apiErr.Status = envelope.Error.Status
	apiErr.Message = envelope.Error.Message
	if len(envelope.Error.Errors) > 0 {
		apiErr.Reason = envelope.Error.Errors[0].Reason
	}
	return apiErr
}

// IsNotFound reports whether err is a Google API 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}
