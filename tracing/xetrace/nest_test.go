package xetrace

import (
	"reflect"
	"testing"
)

// act builds an event carrying a causality activity id. seq is the completion
// sequence SQL Server assigns within one request.
func act(name, activityID string, seq int, sql string) Event {
	return Event{Name: name, ActivityID: activityID, ActivitySeq: seq, SQL: sql}
}

// childSQL flattens one event's children to their SQL for readable assertions.
func childSQL(e Event) []string {
	out := make([]string, len(e.Children))
	for i, c := range e.Children {
		out[i] = c.SQL
	}
	return out
}

func TestNest(t *testing.T) {
	t.Run("inner statements attach to the call that ran them", func(t *testing.T) {
		// XE emits inner statements BEFORE the rpc that contains them, so the
		// input order is body-then-call; the output must be call-owns-body.
		in := []Event{
			act(EventSPStatementCompleted, "A", 1, "WITH T1 AS (SELECT TOP 1 …) SELECT …"),
			act(EventSPStatementCompleted, "A", 2, "SELECT AsDepositValue.fundGuid …"),
			act(EventRPCCompleted, "A", 3, "EXEC asc_GetDepositValueList '9F1C', 100000"),
		}
		got := Nest(in)

		if len(got) != 1 {
			t.Fatalf("expected the rpc alone at top level, got %d events: %v", len(got), sqls(got))
		}
		if got[0].Name != EventRPCCompleted {
			t.Errorf("top-level event = %q, want the rpc", got[0].Name)
		}
		want := []string{
			"WITH T1 AS (SELECT TOP 1 …) SELECT …",
			"SELECT AsDepositValue.fundGuid …",
		}
		if diff := childSQL(got[0]); !reflect.DeepEqual(diff, want) {
			t.Errorf("children = %#v, want %#v", diff, want)
		}
	})

	t.Run("children are ordered by activity sequence not arrival", func(t *testing.T) {
		in := []Event{
			act(EventSPStatementCompleted, "A", 3, "third"),
			act(EventSPStatementCompleted, "A", 1, "first"),
			act(EventRPCCompleted, "A", 9, "EXEC p"),
			act(EventSPStatementCompleted, "A", 2, "second"),
		}
		got := Nest(in)
		want := []string{"first", "second", "third"}
		if diff := childSQL(got[0]); !reflect.DeepEqual(diff, want) {
			t.Errorf("children = %#v, want %#v", diff, want)
		}
	})

	t.Run("events without an activity id are untouched", func(t *testing.T) {
		// The causality-off case: every trace that does not opt into
		// sp_statement_completed must render exactly as it does today.
		in := []Event{
			{Name: EventSQLStatementCompleted, SQL: "SELECT 1"},
			{Name: EventRPCCompleted, SQL: "EXEC p"},
		}
		got := Nest(in)
		if !reflect.DeepEqual(got, in) {
			t.Errorf("Nest() = %v, want input unchanged %v", sqls(got), sqls(in))
		}
		if HasChildren(got) {
			t.Error("HasChildren must be false with no causality data")
		}
	})

	t.Run("orphaned inner statements stay visible at top level", func(t *testing.T) {
		// The parent fell outside the capture window or was filtered out.
		// Swallowing its statements would hide real work.
		in := []Event{
			act(EventSPStatementCompleted, "A", 1, "orphan one"),
			act(EventSPStatementCompleted, "A", 2, "orphan two"),
		}
		got := Nest(in)
		if len(got) != 2 {
			t.Fatalf("orphans must stay flat, got %d: %v", len(got), sqls(got))
		}
		if HasChildren(got) {
			t.Error("orphans must not be given children")
		}
	})

	t.Run("separate activities do not cross-attach", func(t *testing.T) {
		in := []Event{
			act(EventSPStatementCompleted, "A", 1, "a-inner"),
			act(EventRPCCompleted, "A", 2, "EXEC a"),
			act(EventSPStatementCompleted, "B", 1, "b-inner"),
			act(EventRPCCompleted, "B", 2, "EXEC b"),
		}
		got := Nest(in)
		if len(got) != 2 {
			t.Fatalf("expected two parents, got %d: %v", len(got), sqls(got))
		}
		if diff := childSQL(got[0]); !reflect.DeepEqual(diff, []string{"a-inner"}) {
			t.Errorf("first parent children = %#v", diff)
		}
		if diff := childSQL(got[1]); !reflect.DeepEqual(diff, []string{"b-inner"}) {
			t.Errorf("second parent children = %#v", diff)
		}
	})

	t.Run("input is not mutated", func(t *testing.T) {
		in := []Event{
			act(EventSPStatementCompleted, "A", 1, "inner"),
			act(EventRPCCompleted, "A", 2, "EXEC p"),
		}
		_ = Nest(in)
		for i, e := range in {
			if len(e.Children) != 0 {
				t.Errorf("input event %d was mutated with %d children", i, len(e.Children))
			}
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if got := Nest(nil); len(got) != 0 {
			t.Errorf("Nest(nil) = %v, want empty", got)
		}
	})
}
