package xetrace

import "slices"

// objectEventPredicates keep an object event to one committed change a user
// made: the commit phase (ddl_opcode 1 — a DDL statement raises Begin, then
// Commit or Rollback), outside tempdb (database_id 2, where #temp tables live),
// and not the statistics SQL Server creates on its own (object_type 21587,
// STATISTICS). All three are intrinsic fields, so they lead the WHERE.
func objectEventPredicates() []ddlNode {
	return ddlNodes(
		binary("EQ", eventField("ddl_phase"), number(1)),
		binary("NEQ", eventField("database_id"), number(2)),
		binary("NEQ", eventField("object_type"), number(21587)),
	)
}

func isObjectEvent(name string) bool { return slices.Contains(ObjectEvents, name) }
