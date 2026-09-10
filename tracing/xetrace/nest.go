package xetrace

import "sort"

// parentEvents are the event kinds that can own inner statements: a procedure's
// sp_statement_completed rows belong to the rpc_completed (a CallableStatement /
// prepared call) or sql_batch_completed (an ad-hoc EXEC batch) that ran them.
var parentEvents = map[string]struct{}{
	EventRPCCompleted:      {},
	EventSQLBatchCompleted: {},
}

// Nest groups events by their causality activity id and attaches each group's
// inner statements to the call that ran them, returning the parents (and every
// ungrouped event) in the original order.
//
// Inner statements COMPLETE BEFORE the call containing them, so a flat trace
// lists a procedure's body ahead of the procedure itself. Nest reverses that
// into the containment the reader expects.
//
// It is deliberately conservative — anything it cannot attribute is passed
// through unchanged rather than guessed at:
//
//   - an event with no ActivityID (causality off, i.e. every trace that does not
//     opt into sp_statement_completed) stays exactly where it was;
//   - a group with no parent event — the parent fell outside the capture window,
//     or was dropped by a --min-duration/--table filter — stays flat, so its
//     statements are still visible rather than silently swallowed;
//   - a group with several parents keeps the last one as the owner and leaves the
//     others as top-level rows, since nothing in the activity id says which
//     nests inside which.
//
// Children are ordered by ActivitySeq (completion order within the request).
// Nest does not mutate its input.
func Nest(events []Event) []Event {
	if len(events) == 0 {
		return events
	}

	// Index of the owning parent within `events`, per activity id. Only
	// populated for groups that actually have one.
	ownerOf := make(map[string]int)
	for i, e := range events {
		if e.ActivityID == "" {
			continue
		}
		if _, isParent := parentEvents[e.Name]; !isParent {
			continue
		}
		ownerOf[e.ActivityID] = i
	}
	if len(ownerOf) == 0 {
		return events
	}

	children := make(map[int][]Event)
	claimed := make(map[int]struct{})
	for i, e := range events {
		if e.ActivityID == "" {
			continue
		}
		owner, ok := ownerOf[e.ActivityID]
		if !ok || owner == i {
			continue
		}
		children[owner] = append(children[owner], e)
		claimed[i] = struct{}{}
	}
	if len(children) == 0 {
		return events
	}
	for owner, kids := range children {
		sort.SliceStable(kids, func(a, b int) bool { return kids[a].ActivitySeq < kids[b].ActivitySeq })
		children[owner] = kids
	}

	out := make([]Event, 0, len(events))
	for i, e := range events {
		if _, isChild := claimed[i]; isChild {
			continue
		}
		if kids, ok := children[i]; ok {
			e.Children = kids
		}
		out = append(out, e)
	}
	return out
}

// HasChildren reports whether any event in the slice carries nested inner
// statements, i.e. whether Nest found anything to attach. Callers use it to
// decide between flat table and tree rendering.
func HasChildren(events []Event) bool {
	for _, e := range events {
		if len(e.Children) > 0 {
			return true
		}
	}
	return false
}
