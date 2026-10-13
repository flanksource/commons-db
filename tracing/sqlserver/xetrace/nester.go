package xetrace

import "sort"

// defaultPendingChildren bounds how many inner statements streamNester will
// hold while waiting for their parent call. A procedure that emits more than
// this before completing has its buffered statements released early rather
// than growing the buffer without limit — output ordering degrades, capture
// does not.
const defaultPendingChildren = 512

// streamNester reorders a live event stream so a procedure's inner statements
// follow the call that ran them.
//
// XE raises sp_statement_completed as each inner statement finishes, which is
// BEFORE the enclosing rpc_completed — so the raw stream shows a procedure's
// body ahead of the procedure. The nester holds non-parent events carrying a
// causality activity id until that activity's parent arrives, then emits the
// parent followed by its statements in sequence order. Nest() turns that
// ordering into actual containment at render time.
//
// Events with no activity id (every trace that has not opted into
// sp_statement_completed, since causality is off) pass straight through, so the
// default stream is unchanged.
//
// Not safe for concurrent use; Drain owns one per capture.
type streamNester struct {
	pending map[string][]Event
	order   []string // activity ids in first-seen order, for bounded release
	count   int
	limit   int
}

func newStreamNester(limit int) *streamNester {
	if limit <= 0 {
		limit = defaultPendingChildren
	}
	return &streamNester{pending: map[string][]Event{}, limit: limit}
}

// Add routes one event, calling emit for everything that is ready to be
// delivered — possibly zero times (the event was buffered) or several (a parent
// arrived and released its statements).
func (n *streamNester) Add(e Event, emit func(Event)) {
	if e.ActivityID == "" {
		emit(e)
		return
	}
	if _, isParent := parentEvents[e.Name]; isParent {
		emit(e)
		n.release(e.ActivityID, emit)
		return
	}

	if _, seen := n.pending[e.ActivityID]; !seen {
		n.order = append(n.order, e.ActivityID)
	}
	n.pending[e.ActivityID] = append(n.pending[e.ActivityID], e)
	n.count++

	for n.count > n.limit && len(n.order) > 0 {
		n.release(n.order[0], emit)
	}
}

// Flush emits every still-buffered event, in first-seen activity order. Called
// when capture ends so statements whose parent never arrived — filtered out by
// --min-duration, or completing after the window closed — are still reported
// rather than silently dropped.
func (n *streamNester) Flush(emit func(Event)) {
	for len(n.order) > 0 {
		n.release(n.order[0], emit)
	}
}

// release emits one activity's buffered events in sequence order and forgets it.
func (n *streamNester) release(activityID string, emit func(Event)) {
	events := n.pending[activityID]
	delete(n.pending, activityID)
	n.count -= len(events)
	for i, id := range n.order {
		if id == activityID {
			n.order = append(n.order[:i], n.order[i+1:]...)
			break
		}
	}
	sort.SliceStable(events, func(a, b int) bool { return events[a].ActivitySeq < events[b].ActivitySeq })
	for _, e := range events {
		emit(e)
	}
}
