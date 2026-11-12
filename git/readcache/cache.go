package readcache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	rpchttp "github.com/flanksource/clicky/rpc/http"
)

type Store interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}

type Cache struct {
	Store   Store
	mu      sync.Mutex
	flights map[string]*flight
}

type flight struct {
	write       chan struct{}
	invalidated bool
	done        chan struct{}
	value       []byte
	err         error
}

var lookupIDs atomic.Uint64

func (c *Cache) Invalidate(ctx context.Context, key string) error {
	if c == nil || c.Store == nil || key == "" {
		return errors.New("read cache invalidation requires store and key")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	active := c.flights[key]
	delete(c.flights, key)
	c.mu.Unlock()
	if active != nil {
		unlock, err := active.lock(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		active.invalidated = true
	}
	return c.Store.Delete(ctx, key)
}

func (c *Cache) Load(ctx context.Context, key string, retention time.Duration, load func(context.Context) ([]byte, error)) (result []byte, resultErr error) {
	if c == nil || c.Store == nil || key == "" || retention <= 0 || load == nil {
		return nil, errors.New("read cache requires store, key, positive retention and loader")
	}
	event := Event{ID: strconv.FormatUint(lookupIDs.Add(1), 10), Key: key, Disposition: "lookup"}
	started := time.Now()
	defer func() {
		counters := []rpchttp.TimingCounter{{Name: "ops", Value: 1}}
		if resultErr != nil {
			counters = append(counters, rpchttp.TimingCounter{Name: "errors", Value: 1})
		} else {
			counters = append(counters, rpchttp.TimingCounter{Name: event.Disposition, Value: 1})
		}
		rpchttp.AddTiming(ctx, rpchttp.TimingMetric{Name: "git_cache", Duration: time.Since(started), Counters: counters})
	}()
	event.Metadata, _ = ctx.Value(metadataKey{}).(Metadata)
	defer func() { event.Done, event.Err = true, resultErr; report(ctx, event) }()
	report(ctx, event)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, found, err := c.Store.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("read cache %s: %w", key, err)
		}
		if found {
			event.Disposition = "hit"
			return bytes.Clone(value), ctx.Err()
		}
		c.mu.Lock()
		active, joined := c.flights[key]
		if !joined {
			active = &flight{done: make(chan struct{}), write: make(chan struct{}, 1)}
			if c.flights == nil {
				c.flights = map[string]*flight{}
			}
			c.flights[key] = active
		}
		c.mu.Unlock()
		if !joined {
			event.Disposition = "hit"
			return c.fill(ctx, key, retention, func(ctx context.Context) ([]byte, error) {
				event.Disposition = "load"
				report(ctx, event)
				return load(ctx)
			}, active)
		}
		event.Disposition = "joined"
		report(ctx, event)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-active.done:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if errors.Is(active.err, context.Canceled) || errors.Is(active.err, context.DeadlineExceeded) {
				continue
			}
			return bytes.Clone(active.value), active.err
		}
	}
}

func (c *Cache) fill(ctx context.Context, key string, retention time.Duration, load func(context.Context) ([]byte, error), active *flight) (value []byte, err error) {
	finished := false
	defer func() {
		c.mu.Lock()
		if finished {
			active.value, active.err = bytes.Clone(value), err
		} else {
			active.err = errors.New("read cache loader aborted")
		}
		if c.flights[key] == active {
			delete(c.flights, key)
		}
		close(active.done)
		c.mu.Unlock()
	}()
	value, err = c.compute(ctx, key, retention, load, active)
	finished = true
	return value, err
}

func (c *Cache) compute(ctx context.Context, key string, retention time.Duration, load func(context.Context) ([]byte, error), active *flight) ([]byte, error) {
	value, found, err := c.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if !found {
		value, err = load(ctx)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unlock, err := active.lock(ctx)
		if err != nil {
			return nil, err
		}
		defer unlock()
		if !active.invalidated {
			if err := c.Store.Set(ctx, key, bytes.Clone(value), retention); err != nil {
				return nil, fmt.Errorf("write read cache %s: %w", key, err)
			}
		}
	}
	return bytes.Clone(value), ctx.Err()
}

func (f *flight) lock(ctx context.Context) (func(), error) {
	select {
	case f.write <- struct{}{}:
		return func() { <-f.write }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
