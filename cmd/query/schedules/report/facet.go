// Package report renders tabular results to HTML or PDF through facet.
//
// facet is a TSX/React to Chromium print pipeline with no Go SDK, so a caller
// either POSTs to a `facet serve` instance or execs the CLI. The HTTP API is
// preferred whenever a facet URL or connection is configured: the server keeps a
// warm Chromium worker pool, which is the difference between a ~1.4s render and
// a ~11s one, and keeps ~1GB of Chromium out of this process.
package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"time"

	commonshttp "github.com/flanksource/commons/http"
)

// clientTimeoutGrace is how much longer the HTTP client waits than the render
// timeout it asked the server for, so a server that is about to answer is not
// cut off by its own client.
const clientTimeoutGrace = 30 * time.Second

// Server is a facet render service. The zero value means none is configured and
// rendering falls back to the local binary.
type Server struct {
	BaseURL      string
	Token        string
	TimestampURL string

	// Timeout bounds the render. Zero leaves it bounded by the facet server.
	Timeout time.Duration
}

func (s Server) Configured() bool { return s.BaseURL != "" }

// RenderOptions carry the per-render knobs the archive does not.
type RenderOptions struct {
	TimestampURL string
	Timeout      time.Duration
}

// RenderHTTP renders data through a facet server. srcDir is the template
// directory; it is archived and sent with the request, so the server needs no
// prior knowledge of the template.
func RenderHTTP(
	ctx context.Context,
	server Server,
	data any,
	format, srcDir, entryFile string,
	options RenderOptions,
) ([]byte, error) {
	archive, err := BuildArchive(srcDir)
	if err != nil {
		return nil, fmt.Errorf("build report archive: %w", err)
	}

	body, contentType, err := renderRequest(archive, data, format, entryFile, options)
	if err != nil {
		return nil, err
	}

	client := commonshttp.NewClient().BaseURL(server.BaseURL)
	if options.Timeout > 0 {
		client = client.Timeout(options.Timeout + clientTimeoutGrace)
	} else {
		// The facet server bounds the render itself, so the client must not
		// give up first — its own default is two minutes.
		client = client.Timeout(0)
	}
	if server.Token != "" {
		client = client.Header("X-API-Key", server.Token)
	}

	response, err := client.R(ctx).Header("Content-Type", contentType).Post("/render", body)
	if err != nil {
		return nil, fmt.Errorf("facet render request failed: %w", err)
	}
	if !response.IsOK() {
		detail, _ := response.AsString()
		return nil, fmt.Errorf("facet render failed (status %d): %s", response.StatusCode, detail)
	}

	// HTML comes back as the body; every binary format comes back as a pointer
	// to a stored result that has to be fetched separately.
	if format == "html" {
		return io.ReadAll(response.Body)
	}
	return fetchResult(ctx, client, response)
}

func fetchResult(ctx context.Context, client *commonshttp.Client, response *commonshttp.Response) ([]byte, error) {
	payload, err := response.AsJSON()
	if err != nil {
		return nil, fmt.Errorf("parse facet render response: %w", err)
	}
	location, _ := payload["url"].(string)
	if location == "" {
		return nil, fmt.Errorf("facet render response carried no result url")
	}

	result, err := client.R(ctx).Get(location)
	if err != nil {
		return nil, fmt.Errorf("fetch rendered result: %w", err)
	}
	if !result.IsOK() {
		detail, _ := result.AsString()
		return nil, fmt.Errorf("fetching rendered result failed (status %d): %s", result.StatusCode, detail)
	}
	return io.ReadAll(result.Body)
}

// renderRequest builds the multipart body facet's /render endpoint expects:
// the template archive, the data the template is called with, and the options
// naming the format and entry file.
func renderRequest(
	archive []byte,
	data any,
	format, entryFile string,
	options RenderOptions,
) (io.Reader, string, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, "", fmt.Errorf("marshal report data: %w", err)
	}

	renderOptions := map[string]any{"format": format, "entryFile": entryFile}
	if options.TimestampURL != "" {
		renderOptions["timestampUrl"] = options.TimestampURL
	}
	if options.Timeout > 0 {
		renderOptions["timeout"] = options.Timeout.Milliseconds()
	}
	encodedOptions, err := json.Marshal(renderOptions)
	if err != nil {
		return nil, "", fmt.Errorf("marshal render options: %w", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	field, err := writer.CreateFormFile("archive", "report.tar.gz")
	if err != nil {
		return nil, "", fmt.Errorf("create archive field: %w", err)
	}
	if _, err := field.Write(archive); err != nil {
		return nil, "", fmt.Errorf("write archive field: %w", err)
	}
	if err := writer.WriteField("data", string(encoded)); err != nil {
		return nil, "", fmt.Errorf("write data field: %w", err)
	}
	if err := writer.WriteField("options", string(encodedOptions)); err != nil {
		return nil, "", fmt.Errorf("write options field: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart writer: %w", err)
	}
	return &body, writer.FormDataContentType(), nil
}
