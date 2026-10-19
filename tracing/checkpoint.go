package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrCheckpointParams is a checkpoint whose params its kind refuses, or
// never reads: a kind that takes no readings, or reads none of them.
var ErrCheckpointParams = errors.New("invalid checkpoint params")

type checkpointParamsKey struct{}

type checkpointReadKey struct{}

// WithCheckpointParams has the checkpoints marked under ctx carry params, a
// JSON object the checkpointed kind decodes with DecodeCheckpointParams, such
// as which part of the capture to read and why. Empty params, or null, are
// none.
func WithCheckpointParams(ctx context.Context, params json.RawMessage) context.Context {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ctx
	}
	return context.WithValue(ctx, checkpointParamsKey{}, json.RawMessage(trimmed))
}

// DecodeCheckpointParams decodes the params of the checkpoint a Checkpointer
// reads under into into, refusing unknown fields, and reports whether its
// host gave any.
func DecodeCheckpointParams(ctx context.Context, into any) (bool, error) {
	params, _ := ctx.Value(checkpointParamsKey{}).(json.RawMessage)
	if params == nil {
		return false, nil
	}
	if read, ok := ctx.Value(checkpointReadKey{}).(*bool); ok {
		*read = true
	}
	if err := decodeStrict(params, into); err != nil {
		return true, fmt.Errorf("%w: %w", ErrCheckpointParams, err)
	}
	return true, nil
}

// takeCheckpointReading runs read, one checkpoint's reading of kind, refusing
// the params it leaves unread, which the kind would otherwise silently
// ignore. A nil read is a kind that takes no readings.
func takeCheckpointReading(ctx context.Context, kind string, read func(context.Context) error) error {
	params, _ := ctx.Value(checkpointParamsKey{}).(json.RawMessage)
	decoded := false
	if read != nil {
		if err := read(context.WithValue(ctx, checkpointReadKey{}, &decoded)); err != nil {
			return err
		}
	}
	if params != nil && !decoded {
		return fmt.Errorf("%w: kind %s reads no checkpoint params, given %s", ErrCheckpointParams, kind, params)
	}
	return nil
}

// decodeStrict decodes raw into into, refusing unknown fields and data after
// the value.
func decodeStrict(raw []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the params object")
	}
	return nil
}
