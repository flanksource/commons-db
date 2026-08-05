package query

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	commonshttp "github.com/flanksource/commons/http"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
)

const (
	// ReplayKindHTTP is the only supported replay transport today. The field
	// exists so a future kind can be added without breaking stored profiles.
	ReplayKindHTTP = "http"

	// defaultReplayBodySize caps both the previewed request body and the
	// captured response body.
	defaultReplayBodySize = 64 * 1024
)

// ReplaySpec describes how one result row becomes a replayable HTTP request.
// Method, URL, Body and Headers are CEL expressions evaluated against the row
// after the profile's aliases and columns have been applied, so they see the
// same field names the table does.
type ReplaySpec struct {
	// Kind selects the replay transport. Defaults to http.
	Kind string `json:"kind,omitempty" yaml:"kind,omitempty"`

	// Target is the connection the request is sent to. A relative URL requires
	// one; an absolute URL falls back to its own origin.
	Target connection.HTTPConnection `json:"target,omitempty" yaml:"target,omitempty"`

	// Method is a CEL expression yielding the HTTP method. Defaults to POST.
	Method string `json:"method,omitempty" yaml:"method,omitempty"`

	// URL is a CEL expression yielding an absolute URL or a path relative to
	// Target.
	URL string `json:"url,omitempty" yaml:"url,omitempty"`

	// Body is a CEL expression yielding the request body. Non-string values are
	// JSON-encoded.
	Body string `json:"body,omitempty" yaml:"body,omitempty"`

	// Headers maps header names to CEL expressions. A header whose expression
	// yields blank is omitted rather than sent empty.
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// Clone returns a deep copy, so profile merging never aliases a stored spec.
func (s *ReplaySpec) Clone() *ReplaySpec {
	if s == nil {
		return nil
	}
	out := *s
	if s.Headers != nil {
		out.Headers = make(map[string]string, len(s.Headers))
		for name, expression := range s.Headers {
			out.Headers[name] = expression
		}
	}
	return &out
}

// MergeReplaySpec overlays override onto base field by field, so a profile can
// import a replay block and change only its target or one header.
func MergeReplaySpec(base, override *ReplaySpec) *ReplaySpec {
	if override == nil {
		return base.Clone()
	}
	if base == nil {
		return override.Clone()
	}
	merged := base.Clone()
	if override.Kind != "" {
		merged.Kind = override.Kind
	}
	if !override.Target.IsEmpty() {
		merged.Target = override.Target
	}
	if override.Method != "" {
		merged.Method = override.Method
	}
	if override.URL != "" {
		merged.URL = override.URL
	}
	if override.Body != "" {
		merged.Body = override.Body
	}
	for name, expression := range override.Headers {
		if merged.Headers == nil {
			merged.Headers = map[string]string{}
		}
		merged.Headers[name] = expression
	}
	return merged
}

// ReplayBuildOptions selects the row to replay and carries the caller's
// overrides. Every override wins over the profile's CEL expression, so an
// operator can retarget or reshape a single request without editing the profile.
type ReplayBuildOptions struct {
	// Profile supplies the ReplaySpec and the column metadata used to describe
	// the selected row.
	Profile Profile

	// Rows is the executed result the row is selected from.
	Rows []Row

	// Select filters Rows by exact column value. It must narrow to exactly one
	// row; an ambiguous selection is an error, never a silent first-match.
	Select map[string]string

	// DefaultTarget is used when neither the profile nor TargetOverride names
	// one — typically an application-level default connection.
	DefaultTarget connection.HTTPConnection

	// TargetOverride is a connection reference (name or connection://...) or a
	// direct http(s) URL.
	TargetOverride string

	MethodOverride string
	URLOverride    string
	BodyOverride   string

	// Headers are merged over the profile's rendered headers.
	Headers map[string]string

	// MaxBodyPreview caps the previewed body. Defaults to 64KiB.
	MaxBodyPreview int
}

// ReplayRowSummary identifies the selected row in preview output without
// dumping every column.
type ReplayRowSummary struct {
	Index  int            `json:"index"`
	Values map[string]any `json:"values,omitempty"`
}

// ReplayPreview is the fully resolved request, safe to show a user: the URL has
// its credentials stripped and sensitive headers are masked. The unredacted
// values are kept unexported for ExecuteReplay.
type ReplayPreview struct {
	Profile       string            `json:"profile"`
	Row           ReplayRowSummary  `json:"row"`
	Target        string            `json:"target"`
	Method        string            `json:"method"`
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers"`
	BodyPreview   string            `json:"bodyPreview,omitempty"`
	BodyBytes     int               `json:"bodyBytes,omitempty"`
	BodyTruncated bool              `json:"bodyTruncated,omitempty"`

	// Hash covers the method, URL, headers and body. A caller previews, shows
	// the user what will be sent, then sends the hash back with the execute
	// request; a changed hash means the underlying data moved and the user
	// approved something other than what would now be sent.
	Hash string `json:"hash"`

	body    string
	rawURL  string
	headers map[string]string
	target  connection.HTTPConnection
}

// ReplayExecuteResult is the outcome of actually sending the request.
type ReplayExecuteResult struct {
	Preview         ReplayPreview     `json:"preview"`
	StatusCode      int               `json:"statusCode,omitempty"`
	Status          string            `json:"status,omitempty"`
	DurationMS      int64             `json:"durationMs"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
	ResponsePreview string            `json:"responsePreview,omitempty"`
	ResponseBytes   int               `json:"responseBytes,omitempty"`
}

// BuildReplayPreview resolves one row into a concrete HTTP request without
// sending it.
func BuildReplayPreview(ctx context.Context, opts ReplayBuildOptions) (*ReplayPreview, error) {
	spec := opts.Profile.Replay
	if spec == nil {
		spec = &ReplaySpec{}
	}
	if spec.Kind != "" && spec.Kind != ReplayKindHTTP {
		return nil, fmt.Errorf("profile %q replay kind %q is not supported", opts.Profile.Name, spec.Kind)
	}

	row, index, err := selectReplayRow(opts.Rows, opts.Select)
	if err != nil {
		return nil, err
	}

	method := strings.ToUpper(strings.TrimSpace(opts.MethodOverride))
	if method == "" {
		if method, err = evalReplayString(ctx, row, spec.Method, "method"); err != nil {
			return nil, err
		}
	}
	if method == "" {
		method = http.MethodPost
	}

	requestPath := strings.TrimSpace(opts.URLOverride)
	if requestPath == "" {
		if requestPath, err = evalReplayString(ctx, row, spec.URL, "url"); err != nil {
			return nil, err
		}
	}
	if requestPath == "" {
		return nil, fmt.Errorf("profile %q: replay url resolved empty for row %d", opts.Profile.Name, index)
	}

	target, err := resolveReplayTarget(ctx, spec.Target, opts.DefaultTarget, opts.TargetOverride, requestPath)
	if err != nil {
		return nil, err
	}

	body := opts.BodyOverride
	if body == "" && spec.Body != "" {
		value, err := evalReplayValue(ctx, row, spec.Body, "body")
		if err != nil {
			return nil, err
		}
		body = replayBodyString(value)
	}

	headers := map[string]string{}
	for name, expression := range spec.Headers {
		value, err := evalReplayString(ctx, row, expression, "header "+name)
		if err != nil {
			return nil, err
		}
		if value != "" {
			headers[name] = value
		}
	}
	for name, value := range opts.Headers {
		if strings.TrimSpace(name) != "" {
			headers[name] = value
		}
	}

	resolvedURL := resolveReplayURL(target.URL, requestPath)
	if opts.MaxBodyPreview <= 0 {
		opts.MaxBodyPreview = defaultReplayBodySize
	}
	preview := ReplayPreview{
		Profile:   opts.Profile.Name,
		Row:       replayRowSummary(opts.Profile, row, index),
		Target:    sanitizeURLForDisplay(target.URL),
		Method:    method,
		URL:       sanitizeURLForDisplay(resolvedURL),
		Headers:   sanitizeReplayHeaders(headers),
		BodyBytes: len(body),
		Hash:      previewHash(method, resolvedURL, headers, body),
		body:      body,
		rawURL:    resolvedURL,
		headers:   headers,
		target:    target,
	}
	preview.BodyPreview, preview.BodyTruncated = truncateString(body, opts.MaxBodyPreview)
	return &preview, nil
}

// ExecuteReplay sends a previewed request. The caller is responsible for having
// checked the preview hash first.
func ExecuteReplay(ctx context.Context, preview *ReplayPreview) (*ReplayExecuteResult, error) {
	if preview == nil {
		return nil, fmt.Errorf("replay preview is nil")
	}
	client, err := connection.CreateHTTPClient(ctx, preview.target)
	if err != nil {
		return nil, fmt.Errorf("create replay http client: %w", err)
	}
	connection.ApplyHTTPClientObservability(ctx, "query-replay", client, nil)

	request := client.R(ctx)
	for name, value := range preview.headers {
		request = request.Header(name, value)
	}

	start := time.Now()
	response, err := sendReplayRequest(request, preview.Method, preview.rawURL, preview.body)
	if err != nil {
		return nil, fmt.Errorf("execute replay request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, defaultReplayBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("read replay response: %w", err)
	}
	text, truncated := truncateString(string(body), defaultReplayBodySize)
	if truncated {
		text += "\n... truncated"
	}

	return &ReplayExecuteResult{
		Preview:         *preview,
		StatusCode:      response.StatusCode,
		Status:          response.Status,
		DurationMS:      time.Since(start).Milliseconds(),
		ResponseHeaders: sanitizeReplayHeaders(flattenHTTPHeaders(response.Header)),
		ResponsePreview: text,
		ResponseBytes:   len(body),
	}, nil
}

func sendReplayRequest(request *commonshttp.Request, method, url, body string) (*commonshttp.Response, error) {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return request.Get(url)
	case http.MethodPost:
		return request.Post(url, body)
	case http.MethodPut:
		return request.Put(url, body)
	case http.MethodPatch:
		return request.Patch(url, body)
	case http.MethodDelete:
		return request.Delete(url)
	default:
		return nil, fmt.Errorf("unsupported replay method %q", method)
	}
}

// selectReplayRow narrows rows to exactly one. Replaying the wrong row is not
// recoverable, so an ambiguous selection lists the candidates instead of
// guessing.
func selectReplayRow(rows []Row, selector map[string]string) (Row, int, error) {
	type candidate struct {
		row   Row
		index int
	}
	var candidates []candidate
	for index, row := range rows {
		if matchesSelector(row, selector) {
			candidates = append(candidates, candidate{row: row, index: index})
		}
	}
	switch len(candidates) {
	case 0:
		if len(selector) == 0 {
			return nil, 0, fmt.Errorf("no rows to replay")
		}
		return nil, 0, fmt.Errorf("no row matched %s", describeSelector(selector))
	case 1:
		return candidates[0].row, candidates[0].index, nil
	default:
		indexes := make([]string, 0, len(candidates))
		for _, c := range candidates {
			indexes = append(indexes, fmt.Sprint(c.index))
		}
		return nil, 0, fmt.Errorf("%d rows matched %s; narrow the selection (rows %s)",
			len(candidates), describeSelector(selector), strings.Join(indexes, ", "))
	}
}

func matchesSelector(row Row, selector map[string]string) bool {
	for column, want := range selector {
		if fmt.Sprint(row[column]) != want {
			return false
		}
	}
	return true
}

func describeSelector(selector map[string]string) string {
	if len(selector) == 0 {
		return "the result"
	}
	pairs := make([]string, 0, len(selector))
	for column, value := range selector {
		pairs = append(pairs, fmt.Sprintf("%s=%s", column, value))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, " ")
}

// replayRowSummary describes the selected row using the profile's visible
// columns, falling back to the raw row when the profile declares none.
func replayRowSummary(profile Profile, row Row, index int) ReplayRowSummary {
	summary := ReplayRowSummary{Index: index, Values: map[string]any{}}
	if len(profile.Columns) == 0 {
		for name, value := range row {
			summary.Values[name] = value
		}
		return summary
	}
	for _, column := range profile.Columns {
		if column.Hidden {
			continue
		}
		if value, ok := row[column.Name]; ok {
			summary.Values[column.Name] = value
		}
	}
	return summary
}

// resolveReplayTarget picks the connection the request goes to, in precedence
// order: explicit override, profile target, caller default, then the origin of
// an absolute request URL. A relative URL with no target is an error.
func resolveReplayTarget(ctx context.Context, profileTarget, defaultTarget connection.HTTPConnection, override, requestURL string) (connection.HTTPConnection, error) {
	target := profileTarget
	if target.IsEmpty() {
		target = defaultTarget
	}
	if override = strings.TrimSpace(override); override != "" {
		if strings.HasPrefix(override, "http://") || strings.HasPrefix(override, "https://") {
			target = connection.HTTPConnection{URL: override}
		} else {
			target = connection.HTTPConnection{ConnectionName: override}
		}
	}
	if target.IsEmpty() {
		if !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			return connection.HTTPConnection{}, fmt.Errorf("replay target is required for relative url %q", requestURL)
		}
		target = connection.HTTPConnection{URL: originURL(requestURL)}
	}
	if _, err := target.Hydrate(ctx, ctx.GetNamespace()); err != nil {
		return connection.HTTPConnection{}, err
	}
	if target.URL == "" {
		return connection.HTTPConnection{}, fmt.Errorf("replay target resolved without a url")
	}
	return target, nil
}

func originURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment = "", "", "", ""
	return parsed.String()
}

func evalReplayString(ctx context.Context, row Row, expression, field string) (string, error) {
	value, err := evalReplayValue(ctx, row, expression, field)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(NormalizeKeyValue(value)), nil
}

func evalReplayValue(ctx context.Context, row Row, expression, field string) (any, error) {
	if expression = strings.TrimSpace(expression); expression == "" {
		return "", nil
	}
	value, err := evalRowCEL(ctx, expression, row)
	if err != nil {
		return nil, fmt.Errorf("replay %s expression: %w", field, enrichCELError(err, row))
	}
	return value, nil
}

func replayBodyString(value any) string {
	if NormalizeKeyValue(value) == "" {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

func resolveReplayURL(base, request string) string {
	if strings.HasPrefix(request, "http://") || strings.HasPrefix(request, "https://") {
		return request
	}
	if base == "" {
		return request
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(request, "/")
}

func sanitizeURLForDisplay(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	parsed.User = nil
	return parsed.String()
}

func sanitizeReplayHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		if isSensitiveHeader(name) {
			out[name] = "********"
		} else {
			out[name] = value
		}
	}
	return out
}

func isSensitiveHeader(name string) bool {
	for _, sensitive := range logger.SensitiveHeaders {
		if strings.EqualFold(sensitive, name) {
			return true
		}
	}
	return false
}

func flattenHTTPHeaders(headers http.Header) map[string]string {
	out := make(map[string]string, len(headers))
	for name, values := range headers {
		out[name] = strings.Join(values, ", ")
	}
	return out
}

func truncateString(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	return s[:max], true
}

// previewHash covers everything that would be sent, with headers in a stable
// order so the same request always hashes the same.
func previewHash(method, url string, headers map[string]string, body string) string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	digest := sha256.New()
	_, _ = digest.Write([]byte(strings.ToUpper(method)))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(url))
	for _, name := range names {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(strings.ToLower(name)))
		_, _ = digest.Write([]byte("="))
		_, _ = digest.Write([]byte(headers[name]))
	}
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(body))
	return hex.EncodeToString(digest.Sum(nil))
}
