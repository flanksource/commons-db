// The SQL statement tap: observers of a connection, or of every connection,
// receive each statement commons-db runs on it while they observe.

package connection

import (
	"sync"
	"time"
)

// Statement is one SQL statement commons-db ran. Connection names the
// connection it ran on; it is empty for the context's own database.
type Statement struct {
	Connection string
	Driver     string
	SQL        string
	Args       []any
	StartedAt  time.Time
	Duration   time.Duration
	// Rows is how many rows the statement returned, or -1 when unknown.
	Rows  int64
	Error string
}

type sqlObservers struct {
	mu        sync.RWMutex
	next      uint64
	observers map[string]map[uint64]func(Statement)
}

var sqlStatements = &sqlObservers{observers: map[string]map[uint64]func(Statement){}}

// ObserveSQL hands deliver every statement published for connection, or for
// any connection when connection is empty, until release is called. deliver
// runs on the goroutine that ran the statement, so it must not block.
func ObserveSQL(connection string, deliver func(Statement)) (release func()) {
	sqlStatements.mu.Lock()
	sqlStatements.next++
	id := sqlStatements.next
	if sqlStatements.observers[connection] == nil {
		sqlStatements.observers[connection] = map[uint64]func(Statement){}
	}
	sqlStatements.observers[connection][id] = deliver
	sqlStatements.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			sqlStatements.mu.Lock()
			defer sqlStatements.mu.Unlock()
			delete(sqlStatements.observers[connection], id)
			if len(sqlStatements.observers[connection]) == 0 {
				delete(sqlStatements.observers, connection)
			}
		})
	}
}

// ObservingSQL reports whether anyone observes connection's statements, so a
// publisher can skip describing a statement nobody receives.
func ObservingSQL(connection string) bool {
	sqlStatements.mu.RLock()
	defer sqlStatements.mu.RUnlock()
	return len(sqlStatements.observers[connection]) > 0 || len(sqlStatements.observers[""]) > 0
}

// PublishSQL hands statement to the observers of its connection and to those
// of every connection.
func PublishSQL(statement Statement) {
	sqlStatements.mu.RLock()
	var delivers []func(Statement)
	for _, deliver := range sqlStatements.observers[statement.Connection] {
		delivers = append(delivers, deliver)
	}
	if statement.Connection != "" {
		for _, deliver := range sqlStatements.observers[""] {
			delivers = append(delivers, deliver)
		}
	}
	sqlStatements.mu.RUnlock()
	for _, deliver := range delivers {
		deliver(statement)
	}
}
