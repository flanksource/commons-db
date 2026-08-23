package opensearch

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v2"
	"github.com/opensearch-project/opensearch-go/v2/opensearchapi"
	"github.com/opensearch-project/opensearch-go/v2/opensearchtransport"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons/logger"
)

type Searcher struct {
	client        *opensearch.Client
	config        *Backend
	mappingConfig *logs.FieldMappingConfig
}

type RawClientMixin interface {
	GetRawClient() any
}

func (t *Searcher) GetRawClient() *opensearch.Client {
	return t.client
}

func (t *Searcher) InspectionKey() string {
	if t.config.InspectionKey != "" {
		return t.config.InspectionKey
	}
	digest := sha256.Sum256([]byte(t.config.Address))
	return fmt.Sprintf("opensearch:%x", digest)
}

func New(ctx context.Context, backend Backend, mappingConfig *logs.FieldMappingConfig) (*Searcher, error) {
	return NewWithTransport(ctx, backend, mappingConfig, nil)
}

// NewWithTransport creates an OpenSearch client using a caller-provided HTTP
// transport. Connection-backed callers use this to share Basic, OAuth and mTLS
// behavior with the generic HTTP connection layer; direct log backends retain
// the legacy username/password configuration through New.
func NewWithTransport(ctx context.Context, backend Backend, mappingConfig *logs.FieldMappingConfig, base http.RoundTripper) (*Searcher, error) {
	if base == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if backend.InsecureTLS {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit per-connection opt-in
		}
		base = transport
	}
	cfg := opensearch.Config{
		Addresses: []string{backend.Address},
		// Maintain HAR capture / HTTP logging for the "opensearch" feature.
		Transport: connection.ApplyHTTPObservability(ctx, "opensearch", base, nil),
	}

	if ctx.Logger.V(3).Enabled() {
		cfg.Logger = &opensearchtransport.ColorLogger{
			Output: os.Stderr,
		}
	}

	if backend.Username != nil {
		username, err := ctx.GetEnvValueFromCache(*backend.Username, ctx.GetNamespace())
		if err != nil {
			return nil, ctx.Oops().Wrapf(err, "error getting the openSearch config")
		}
		cfg.Username = username
	}

	if backend.Password != nil {
		password, err := ctx.GetEnvValueFromCache(*backend.Password, ctx.GetNamespace())
		if err != nil {
			return nil, ctx.Oops().Wrapf(err, "error getting the openSearch config")
		}
		cfg.Password = password
	}

	client, err := opensearch.NewClient(cfg)
	if err != nil {
		return nil, ctx.Oops().Wrapf(err, "error creating the openSearch client")
	}

	pingResp, err := client.Ping()
	if err != nil {
		return nil, ctx.Oops().Wrapf(err, "error pinging the openSearch client")
	}

	if pingResp.StatusCode != 200 {
		return nil, ctx.Oops().Errorf("[opensearch] got ping response: %d", pingResp.StatusCode)
	}

	return &Searcher{
		client:        client,
		config:        &backend,
		mappingConfig: mappingConfig,
	}, nil
}

func (t *Searcher) Search(ctx context.Context, q Request) (*logs.LogResult, error) {
	r, err := t.SearchRaw(ctx, q)
	if err != nil {
		return nil, err
	}
	return t.parseSearchResponse(ctx, r), nil
}

// SearchRaw executes the same OpenSearch request as Search but preserves the
// native hit, aggregation and timing envelope. Connection browsers use this to
// inspect arbitrary documents; log callers continue through Search's mapping.
func (t *Searcher) SearchRaw(ctx context.Context, q Request) (Response, error) {
	if q.Index == "" && q.PIT == "" {
		return Response{}, ctx.Oops().Errorf("index is empty")
	}

	// An unset limit used to mean 500, which made "read everything" and "read
	// the first 500" indistinguishable to every caller and every reader of the
	// result. How many documents to ask for is the caller's decision, and it is
	// now required to make it.
	if q.Limit == "" {
		return Response{}, ctx.Oops().Errorf("search limit is required; pass \"0\" to request no documents (an aggregation-only search)")
	}
	limit, err := strconv.Atoi(q.Limit)
	if err != nil {
		return Response{}, ctx.Oops().Wrapf(err, "error converting limit to int")
	}

	logger.Tracef("searching index %s with query %s", q.Index, q.Query)

	options := []func(*opensearchapi.SearchRequest){
		t.client.Search.WithContext(ctx),
		t.client.Search.WithBody(strings.NewReader(q.Query)),
		t.client.Search.WithSize(limit),
	}
	// A point-in-time already names the indices it was opened over, and
	// OpenSearch rejects a search that names them again.
	if q.PIT == "" {
		options = append(options, t.client.Search.WithIndex(q.Index))
	}

	res, err := t.client.Search(options...)
	if err != nil {
		return Response{}, ctx.Oops().Wrapf(err, "error searching")
	}
	defer func() { _ = res.Body.Close() }()

	if res.IsError() {
		return Response{}, readOpenSearchError("search", res.StatusCode, res.Status(), res.Body)
	}

	var r Response
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return Response{}, ctx.Oops().Wrapf(err, "error parsing the response body")
	}
	return r, nil
}

var DefaultFieldMappingConfig = logs.FieldMappingConfig{
	Message:   []string{"message"},
	Timestamp: []string{"@timestamp"},
	Severity:  []string{"log"},
}

// OpenPIT opens a point-in-time over index and returns its id.
//
// A PIT is what makes a walk a walk rather than a series of unrelated searches:
// every page reads the same frozen view, so a document written or merged
// mid-walk cannot shift the rows under a position already handed out. Without
// one, search_after is stable against ties but still sees the index change.
func (t *Searcher) OpenPIT(ctx context.Context, index string, keepAlive time.Duration) (string, error) {
	if index == "" {
		return "", ctx.Oops().Errorf("index is empty")
	}
	if keepAlive <= 0 {
		keepAlive = DefaultPITKeepAlive
	}
	res, created, err := t.client.PointInTime.Create(
		t.client.PointInTime.Create.WithContext(ctx),
		t.client.PointInTime.Create.WithIndex(index),
		t.client.PointInTime.Create.WithKeepAlive(keepAlive),
	)
	if err != nil {
		return "", ctx.Oops().Wrapf(err, "error opening point-in-time")
	}
	if res != nil {
		defer func() { _ = res.Body.Close() }()
		if res.IsError() {
			return "", readOpenSearchError("open point-in-time", res.StatusCode, res.Status(), res.Body)
		}
	}
	if created == nil || created.PitID == "" {
		return "", ctx.Oops().Errorf("opensearch returned an empty point-in-time id")
	}
	return created.PitID, nil
}

// ClosePIT releases a point-in-time. A PIT holds segments open until it expires,
// so a walk that ends early says so rather than leaving the cluster to time it
// out.
func (t *Searcher) ClosePIT(ctx context.Context, pitID string) error {
	if pitID == "" {
		return nil
	}
	res, _, err := t.client.PointInTime.Delete(
		t.client.PointInTime.Delete.WithContext(ctx),
		t.client.PointInTime.Delete.WithPitID(pitID),
	)
	if err != nil {
		return ctx.Oops().Wrapf(err, "error closing point-in-time")
	}
	if res == nil {
		return nil
	}
	defer func() { _ = res.Body.Close() }()
	if res.IsError() {
		return readOpenSearchError("close point-in-time", res.StatusCode, res.Status(), res.Body)
	}
	return nil
}

// preprocessJSONFields attempts to unmarshal JSON from fields ending with @json or @input
// It modifies the input map in place, replacing string values with unmarshalled JSON where possible
func preprocessJSONFields(source map[string]any) {
	for key, value := range source {
		// Check if field name ends with @json or @input
		if !strings.HasSuffix(key, "@json") && !strings.HasSuffix(key, "@input") {
			continue
		}

		// Only attempt to unmarshal string values
		strValue, ok := value.(string)
		if !ok {
			continue
		}

		// Attempt to unmarshal the JSON string
		var jsonValue any
		if err := json.Unmarshal([]byte(strValue), &jsonValue); err == nil {
			// Successfully unmarshalled, replace the value
			source[key] = jsonValue
		}
		// On error, leave the original string value unchanged (treat as text)
	}
}

// ParseResponse maps a raw search response to log lines, one per hit and in hit
// order.
//
// It is exported so a caller needing both the mapped rows and the raw hits —
// their sort values for a cursor, the total for a footer — can map a response
// it already holds, rather than searching twice or being handed rows whose
// positions have been thrown away.
func (t *Searcher) ParseResponse(ctx context.Context, r Response) *logs.LogResult {
	return t.parseSearchResponse(ctx, r)
}

// parseSearchResponse extracts log lines from search response
func (t *Searcher) parseSearchResponse(ctx context.Context, r Response) *logs.LogResult {
	var logResult = logs.LogResult{}
	logResult.Logs = make([]*logs.LogLine, 0, len(r.Hits.Hits))

	if len(r.Aggregations) > 0 {
		if logResult.Metadata == nil {
			logResult.Metadata = make(map[string]any)
		}
		logResult.Metadata["aggregations"] = r.Aggregations
	}

	mappingConfig := DefaultFieldMappingConfig
	if t.mappingConfig != nil {
		mappingConfig = t.mappingConfig.WithDefaults(DefaultFieldMappingConfig)
	}

	for _, hit := range r.Hits.Hits {
		line := &logs.LogLine{
			ID:    hit.ID,
			Count: 1,
		}

		// Preprocess JSON fields to unmarshal @json and @input suffixed fields
		preprocessJSONFields(hit.Source)

		for k, v := range hit.Source {
			if err := logs.MapFieldToLogLine(k, v, line, mappingConfig); err != nil {
				// Log or handle mapping error? For now, just log it.
				ctx.Warnf("Error mapping field %s for log %s: %v", k, line.ID, err)
			}
		}

		line.SetHash()
		logResult.Logs = append(logResult.Logs, line)
	}

	return &logResult
}
