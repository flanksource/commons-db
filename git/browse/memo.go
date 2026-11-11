package browse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	rpchttp "github.com/flanksource/clicky/rpc/http"
)

type readMemo[T any] struct {
	mu      sync.Mutex
	entries map[string]*memoEntry[T]
}

func (m *readMemo[T]) delete(key string) {
	m.mu.Lock()
	delete(m.entries, key)
	m.mu.Unlock()
}

type memoEntry[T any] struct {
	done    chan struct{}
	value   T
	err     error
	expires time.Time
}
type memoLoad[T any] struct {
	key    string
	metric string
	owner  context.Context
	load   func() (T, error)
}

func (m *readMemo[T]) get(ctx context.Context, request memoLoad[T]) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := request.owner.Err(); err != nil {
		return zero, err
	}
	m.mu.Lock()
	if m.entries == nil {
		m.entries = make(map[string]*memoEntry[T])
	}
	if entry := m.entries[request.key]; entry != nil && !entry.expires.IsZero() && !time.Now().Before(entry.expires) {
		delete(m.entries, request.key)
	}
	if entry := m.entries[request.key]; entry != nil {
		ready := !entry.expires.IsZero()
		m.mu.Unlock()
		stopWait := func() {}
		if !ready {
			rpchttp.AddTiming(request.owner, rpchttp.TimingMetric{Name: "release_reuse", Counters: []rpchttp.TimingCounter{{Name: request.metric + "_joined", Value: 1}}})
			stopWait = sync.OnceFunc(rpchttp.Track(request.owner, "git_"+request.metric+"_wait"))
			defer stopWait()
		}
		select {
		case <-entry.done:
			stopWait()
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			if errors.Is(entry.err, context.Canceled) || errors.Is(entry.err, context.DeadlineExceeded) {
				return m.get(ctx, request)
			}
			if ready && entry.err == nil {
				rpchttp.AddTiming(request.owner, rpchttp.TimingMetric{Name: "release_reuse", Counters: []rpchttp.TimingCounter{{Name: request.metric + "_hits", Value: 1}}})
			}
			return entry.value, entry.err
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-request.owner.Done():
			return zero, request.owner.Err()
		}
	}
	entry := &memoEntry[T]{done: make(chan struct{})}
	m.entries[request.key] = entry
	m.mu.Unlock()
	rpchttp.AddTiming(request.owner, rpchttp.TimingMetric{Name: "release_reuse", Counters: []rpchttp.TimingCounter{{Name: request.metric + "_loads", Value: 1}}})
	completed := false
	defer func() {
		if !completed {
			entry.err = fmt.Errorf("git %s loading aborted", request.metric)
		}
		if entry.err != nil {
			m.mu.Lock()
			delete(m.entries, request.key)
			m.mu.Unlock()
		}
		m.mu.Lock()
		entry.expires = time.Now().Add(30 * time.Second)
		close(entry.done)
		m.mu.Unlock()
	}()
	entry.value, entry.err = request.load()
	entry.err = errors.Join(entry.err, ctx.Err())
	completed = true
	return entry.value, entry.err
}
