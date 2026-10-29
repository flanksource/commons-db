package providers

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query"
	promAPI "github.com/prometheus/client_golang/api"
	promV1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

type annotatedPrometheus struct {
	promV1.API
	client   promAPI.Client
	metadata *query.MetricMetadata
}

func (c annotatedPrometheus) Query(ctx stdctx.Context, expression string, t time.Time, _ ...promV1.Option) (model.Value, promV1.Warnings, error) {
	return c.read(ctx, "/api/v1/query", url.Values{"query": {expression}, "time": {strconv.FormatFloat(float64(t.UnixMilli())/1000, 'f', 3, 64)}})
}
func (c annotatedPrometheus) QueryRange(ctx stdctx.Context, expression string, r promV1.Range, _ ...promV1.Option) (model.Value, promV1.Warnings, error) {
	return c.read(ctx, "/api/v1/query_range", url.Values{"query": {expression}, "start": {strconv.FormatFloat(float64(r.Start.UnixMilli())/1000, 'f', 3, 64)}, "end": {strconv.FormatFloat(float64(r.End.UnixMilli())/1000, 'f', 3, 64)}, "step": {strconv.FormatFloat(r.Step.Seconds(), 'f', -1, 64)}})
}
func (c annotatedPrometheus) read(ctx stdctx.Context, path string, params url.Values) (model.Value, promV1.Warnings, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.client.URL(path, nil).String(), strings.NewReader(params.Encode()))
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, body, err := c.client.Do(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	var envelope struct {
		Status   string   `json:"status"`
		Error    string   `json:"error"`
		Warnings []string `json:"warnings"`
		Infos    []string `json:"infos"`
		Data     struct {
			Type   model.ValueType `json:"resultType"`
			Result json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, nil, fmt.Errorf("decode Prometheus response: %w", err)
	}
	if response.StatusCode/100 != 2 || envelope.Status != "success" {
		return nil, nil, fmt.Errorf("Prometheus status %d: %s", response.StatusCode, envelope.Error)
	}
	if c.metadata != nil {
		for _, msg := range envelope.Warnings {
			c.metadata.Notices = append(c.metadata.Notices, query.QueryNotice{Severity: "warning", Message: msg})
		}
		for _, msg := range envelope.Infos {
			c.metadata.Notices = append(c.metadata.Notices, query.QueryNotice{Severity: "info", Message: msg})
		}
	}
	var value model.Value
	switch envelope.Data.Type {
	case model.ValMatrix:
		value = &model.Matrix{}
	case model.ValVector:
		value = &model.Vector{}
	case model.ValScalar:
		value = &model.Scalar{}
	case model.ValString:
		value = &model.String{}
	default:
		return nil, nil, fmt.Errorf("unsupported Prometheus result type %q", envelope.Data.Type)
	}
	if err := json.Unmarshal(envelope.Data.Result, value); err != nil {
		return nil, nil, err
	}
	switch v := value.(type) {
	case *model.Matrix:
		return *v, envelope.Warnings, nil
	case *model.Vector:
		return *v, envelope.Warnings, nil
	}
	return value, envelope.Warnings, nil
}
