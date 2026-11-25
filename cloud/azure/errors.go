package azure

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	commonshttp "github.com/flanksource/commons/http"
)

// APIError is an error returned by an Azure REST API. Code and Message come
// from the {"error":{"code","message"}} envelope; when the body is not an
// envelope, Message holds the (truncated) raw body.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("azure API error: HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("azure API error: HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// IsNotFound reports whether err is an Azure API 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// ErrorDetail is the Azure error envelope payload.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const maxErrorBody = 512

func newAPIError(resp *commonshttp.Response) *APIError {
	apiErr := &APIError{StatusCode: resp.StatusCode}
	raw, err := resp.AsString()
	if err != nil {
		apiErr.Message = fmt.Sprintf("reading error body: %v", err)
		return apiErr
	}
	var envelope struct {
		Error *ErrorDetail `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &envelope) == nil && envelope.Error != nil {
		apiErr.Code, apiErr.Message = envelope.Error.Code, envelope.Error.Message
		return apiErr
	}
	raw = strings.TrimSpace(raw)
	if len(raw) > maxErrorBody {
		raw = raw[:maxErrorBody] + "..."
	}
	apiErr.Message = raw
	return apiErr
}
