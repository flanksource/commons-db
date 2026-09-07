package senders

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// responseBodyLimit bounds how much of a failed response is quoted back in an
// error. Enough to carry an API's own message, not enough to paste a page of
// HTML into a log line.
const responseBodyLimit = 4096

func bytesReader(body []byte) io.Reader { return bytes.NewReader(body) }

// do performs the request and converts a non-2xx into an error naming the
// channel and quoting the response.
//
// Every sender goes through here so one channel cannot quietly accept a status
// another rejects — the difference between 200-only and 2xx was the sort of
// inconsistency that made a webhook look healthy while dropping messages.
func do(channel string, request *http.Request) error {
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", channel, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, responseBodyLimit))
		return fmt.Errorf("%s returned %d: %s", channel, response.StatusCode, string(body))
	}
	return nil
}
