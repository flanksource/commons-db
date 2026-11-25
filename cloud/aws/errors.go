package aws

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"

	commonshttp "github.com/flanksource/commons/http"
)

// APIError is a non-2xx response from an AWS service, parsed from either the
// JSON (`__type`/`message`) or the XML (`<Error><Code>`) error protocol.
type APIError struct {
	Service    string
	Operation  string
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("aws %s %s: HTTP %d %s: %s (request id %s)", e.Service, e.Operation, e.StatusCode, e.Code, e.Message, e.RequestID)
}

// Is makes a not-found response match fs.ErrNotExist.
func (e *APIError) Is(target error) bool {
	return target == fs.ErrNotExist && e.notFound()
}

func (e *APIError) notFound() bool {
	switch e.Code {
	case "NoSuchKey", "NotFound", "NoSuchBucket", "NotFoundException":
		return true
	}
	return e.Code == "" && e.StatusCode == http.StatusNotFound
}

// IsNotFound reports whether err is an AWS response saying the object, bucket
// or key does not exist: S3 NoSuchKey/NoSuchBucket/404 and KMS NotFoundException.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.notFound()
}

// throttleCodes are the error codes AWS protocols use for rate limiting.
var throttleCodes = map[string]bool{
	"ThrottlingException": true, "Throttling": true, "ThrottledException": true, "RequestLimitExceeded": true,
	"TooManyRequestsException": true, "ProvisionedThroughputExceededException": true, "RequestThrottled": true, "SlowDown": true,
}

// maxErrorPeek bounds how much of a 400 body is read to find its error code.
const maxErrorPeek = 64 << 10

// isThrottled reports whether resp is an HTTP 400 carrying a throttling error
// code. It reads the head of the body to find the code and puts it back, so the
// caller still sees the whole body. A throttled response's body is closed, as
// the attempt is about to be retried.
func isThrottled(resp *commonshttp.Response) bool {
	if resp == nil || resp.Response == nil || resp.StatusCode != http.StatusBadRequest {
		return false
	}
	head := []byte{}
	if resp.Body != nil {
		original := resp.Body
		var readErr error
		head, readErr = io.ReadAll(io.LimitReader(original, maxErrorPeek))
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), original), original}
		if readErr != nil {
			// the caller's own read of the body hits the same error and reports it
			return false
		}
	}
	if !throttleCodes[newAPIError("", "", resp.Response, head).Code] {
		return false
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	return true
}

type xmlErrorBody struct {
	Code      string `xml:"Code"`
	Message   string `xml:"Message"`
	RequestID string `xml:"RequestId"`
	Nested    struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
	NestedRequestID string `xml:"RequestID"`
}

type jsonErrorBody struct {
	Type       string `json:"__type"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	MessageCap string `json:"Message"`
}

// newAPIError builds the error for a non-2xx response. The body protocol is
// detected from its first byte; an unparseable body still yields the status code.
func newAPIError(service, operation string, resp *http.Response, body []byte) *APIError {
	apiErr := &APIError{Service: service, Operation: operation, StatusCode: resp.StatusCode, RequestID: requestID(resp.Header)}
	trimmed := strings.TrimSpace(string(body))
	switch {
	case strings.HasPrefix(trimmed, "{"):
		fillFromJSON(apiErr, body, resp.Header)
	case strings.HasPrefix(trimmed, "<"):
		fillFromXML(apiErr, body)
	}
	if apiErr.Code == "" {
		apiErr.Code = resp.Header.Get("X-Amz-Error-Code")
	}
	return apiErr
}

func requestID(h http.Header) string {
	for _, name := range []string{"X-Amzn-Requestid", "X-Amz-Request-Id"} {
		if v := h.Get(name); v != "" {
			return v
		}
	}
	return ""
}

func fillFromJSON(apiErr *APIError, body []byte, h http.Header) {
	var parsed jsonErrorBody
	if json.Unmarshal(body, &parsed) != nil {
		return
	}
	code := firstNonEmpty(h.Get("X-Amzn-Errortype"), parsed.Type, parsed.Code)
	if i := strings.LastIndex(code, "#"); i >= 0 {
		code = code[i+1:]
	}
	// some services append `:http://...` to the error type
	code, _, _ = strings.Cut(code, ":")
	apiErr.Code = code
	apiErr.Message = firstNonEmpty(parsed.Message, parsed.MessageCap)
}

func fillFromXML(apiErr *APIError, body []byte) {
	var parsed xmlErrorBody
	if xml.Unmarshal(body, &parsed) != nil {
		return
	}
	apiErr.Code = firstNonEmpty(parsed.Code, parsed.Nested.Code)
	apiErr.Message = firstNonEmpty(parsed.Message, parsed.Nested.Message)
	if apiErr.RequestID == "" {
		apiErr.RequestID = firstNonEmpty(parsed.RequestID, parsed.NestedRequestID)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
