package loki

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	netHTTP "net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/samber/lo"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
)

type lokiSearcher struct {
	conn          connection.Loki
	mappingConfig *logs.FieldMappingConfig
}

func New(conn connection.Loki, mappingConfig *logs.FieldMappingConfig) *lokiSearcher {
	return &lokiSearcher{
		conn:          conn,
		mappingConfig: mappingConfig,
	}
}

func (t *lokiSearcher) Search(ctx context.Context, request Request) (*logs.LogResult, error) {
	if err := t.conn.Populate(ctx); err != nil {
		return nil, fmt.Errorf("failed to populate connection: %w", err)
	}

	parsedBaseURL, err := url.Parse(t.conn.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse base URL '%s': %w", t.conn.URL, err)
	}
	apiURL := parsedBaseURL.JoinPath("/loki/api/v1/query_range")
	apiURL.RawQuery = request.Params().Encode()

	// CreateHTTPClient applies whichever authentication the connection carries —
	// basic, bearer, OAuth or mTLS — rather than basic alone.
	client, err := connection.CreateHTTPClient(ctx, t.conn.HTTPConnection)
	if err != nil {
		return nil, fmt.Errorf("failed to create http client: %w", err)
	}
	// Maintain HAR capture / HTTP logging for the "loki" feature.
	connection.ApplyHTTPClientObservability(ctx, "loki", client, nil)

	resp, err := client.R(ctx).Get(apiURL.String())
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	response, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var lokiResp Response
	if err := json.Unmarshal(response, &lokiResp); err != nil {
		return nil, fmt.Errorf("%s", lo.Ellipsis(string(response), 256))
	}

	if resp.StatusCode != netHTTP.StatusOK {
		return nil, fmt.Errorf("loki request failed with status %s: (error: %s, errorType: %s)", resp.Status, lokiResp.Error, lokiResp.ErrorType)
	}

	mappingConfig := DefaultFieldMappingConfig
	if t.mappingConfig != nil {
		mappingConfig = t.mappingConfig.WithDefaults(DefaultFieldMappingConfig)
	}

	result := lokiResp.ToLogResult(mappingConfig)

	// ToLogResult seeds Metadata from Loki's stats, which are absent on a query
	// that matched nothing — hence the guard.
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["query"] = request.Query

	return &result, nil
}

func (t *lokiSearcher) Stream(ctx context.Context, request StreamRequest) (<-chan StreamItem, error) {
	if err := t.conn.Populate(ctx); err != nil {
		return nil, fmt.Errorf("failed to populate connection: %w", err)
	}

	parsedBaseURL, err := url.Parse(t.conn.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse base URL '%s': %w", t.conn.URL, err)
	}

	wsScheme := "ws"
	if parsedBaseURL.Scheme == "https" {
		wsScheme = "wss"
	}
	wsURL := &url.URL{
		Scheme:   wsScheme,
		Host:     parsedBaseURL.Host,
		Path:     "/loki/api/v1/tail",
		RawQuery: request.Params().Encode(),
	}

	dialer := *websocket.DefaultDialer
	if !t.conn.TLS.IsEmpty() {
		if dialer.TLSClientConfig, err = t.conn.TLS.TLSClientConfig(); err != nil {
			return nil, err
		}
	}

	headers := netHTTP.Header{}
	switch {
	case !t.conn.HTTPBasicAuth.IsEmpty():
		auth := t.conn.GetUsername() + ":" + t.conn.GetPassword()
		headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(auth)))
	case !t.conn.Bearer.IsEmpty():
		headers.Set("Authorization", "Bearer "+t.conn.Bearer.ValueStatic)
	case !t.conn.OAuth.IsEmpty():
		// The OAuth exchange lives in an http.RoundTripper, which a websocket
		// handshake never runs. Saying so beats tailing unauthenticated.
		return nil, fmt.Errorf("loki tail does not support OAuth connections; use basic auth, a bearer token or mTLS")
	}

	conn, _, err := dialer.DialContext(ctx, wsURL.String(), headers)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to websocket: %w", err)
	}

	itemChan := make(chan StreamItem)

	go func() {
		defer close(itemChan)
		defer conn.Close()

		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		})

		for {
			select {
			case <-ctx.Done():
				return
			default:
				var response StreamResponse
				if err := conn.ReadJSON(&response); err != nil {
					if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						return
					}
					select {
					case itemChan <- StreamItem{Error: fmt.Errorf("WebSocket read error in Loki stream: %w", err)}:
					case <-ctx.Done():
					}
					return
				}

				mappingConfig := DefaultFieldMappingConfig
				if t.mappingConfig != nil {
					mappingConfig = t.mappingConfig.WithDefaults(DefaultFieldMappingConfig)
				}

				for _, stream := range response.Streams {
					for _, v := range stream.Values {
						if len(v) != 2 {
							continue
						}

						firstObserved, err := strconv.ParseInt(v[0], 10, 64)
						if err != nil {
							continue
						}

						line := &logs.LogLine{
							Count:         1,
							FirstObserved: time.Unix(0, firstObserved),
							Message:       v[1],
							// Cloned: every line of a stream would otherwise
							// share one map, so editing one line's labels
							// would edit them all.
							Labels: maps.Clone(stream.Stream),
						}

						for k, val := range stream.Stream {
							if err := logs.MapFieldToLogLine(k, val, line, mappingConfig); err != nil {
								continue
							}
						}

						line.SetHash()

						select {
						case itemChan <- StreamItem{LogLine: line}:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}
	}()

	return itemChan, nil
}

var DefaultFieldMappingConfig = logs.FieldMappingConfig{
	Severity: []string{"detected_level", "level"},
	Host:     []string{"pod"},
}
