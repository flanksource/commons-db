// One Store per path within a process: a second Open of a path shares the
// first's, so a process never waits on its own lock or spools to itself.
package owner

import (
	"fmt"
	"sync"
)

var (
	sharedMu sync.Mutex
	shared   = map[string]*sharedStore{}
)

type sharedStore struct {
	store any
	refs  int
}

// share returns the Store open for configured, or starts one. Opens of one
// path wait for each other.
func share[T Target](configured string, start func() (*Store[T], error)) (*Store[T], error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if entry, ok := shared[configured]; ok {
		store, ok := entry.store.(*Store[T])
		if !ok {
			return nil, fmt.Errorf("record store owner: %s is already open in this process over another backend type", configured)
		}
		entry.refs++
		return store, nil
	}
	store, err := start()
	if err != nil {
		return nil, err
	}
	shared[configured] = &sharedStore{store: store, refs: 1}
	return store, nil
}

// release drops one reference to configured's Store, reporting whether it was
// the last.
func release(configured string) bool {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	entry, ok := shared[configured]
	if !ok {
		return true
	}
	entry.refs--
	if entry.refs > 0 {
		return false
	}
	delete(shared, configured)
	return true
}
