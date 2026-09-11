package recordstore

import "sync"

// StreamLocks serializes work on one stream while leaving other streams free,
// which is how a backend keeps the single-writer contract inside one process.
// The zero value is ready to use. A lock nobody holds or waits on is dropped,
// so the set does not grow with every stream ever written.
type StreamLocks struct {
	mu    sync.Mutex
	locks map[string]*streamLock
}

type streamLock struct {
	sync.Mutex
	refs int
}

// Lock blocks until stream is free and returns the function that frees it.
func (l *StreamLocks) Lock(stream string) (unlock func()) {
	lock := l.acquire(stream)
	lock.Lock()
	return l.releaser(stream, lock)
}

// TryLock takes stream only when it is free, for work that should leave a
// stream in use alone rather than wait for it.
func (l *StreamLocks) TryLock(stream string) (unlock func(), ok bool) {
	lock := l.acquire(stream)
	if !lock.TryLock() {
		l.drop(stream, lock)
		return nil, false
	}
	return l.releaser(stream, lock), true
}

func (l *StreamLocks) acquire(stream string) *streamLock {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locks == nil {
		l.locks = map[string]*streamLock{}
	}
	lock := l.locks[stream]
	if lock == nil {
		lock = &streamLock{}
		l.locks[stream] = lock
	}
	lock.refs++
	return lock
}

func (l *StreamLocks) releaser(stream string, lock *streamLock) func() {
	return func() {
		lock.Unlock()
		l.drop(stream, lock)
	}
}

func (l *StreamLocks) drop(stream string, lock *streamLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock.refs--
	if lock.refs == 0 {
		delete(l.locks, stream)
	}
}
