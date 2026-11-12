package sqlite

import (
	"sync"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

type sqlNoticesKey struct{}

type sqlNotices struct {
	mu      sync.Mutex
	receive func(string)
}

func withSQLNotices(ctx context.Context) context.Context {
	return ctx.WithValue(sqlNoticesKey{}, &sqlNotices{receive: func(message string) {
		if observe := query.BatchObserver(ctx); observe != nil {
			observe(query.BatchEvent{Message: message})
		}
	}})
}

func (n *sqlNotices) send(message string) {
	n.mu.Lock()
	receive := n.receive
	n.mu.Unlock()
	receive(message)
}

func (n *sqlNotices) listen(receive func(string)) func() {
	n.mu.Lock()
	previous := n.receive
	n.receive = receive
	n.mu.Unlock()
	return func() {
		n.mu.Lock()
		n.receive = previous
		n.mu.Unlock()
	}
}
