package xetrace

import "slices"

// objectEventPredicates keep an object event to one committed change a user
// made: the commit phase (ddl_opcode 1 — a DDL statement raises Begin, then
// Commit or Rollback), outside tempdb (database_id 2, where #temp tables live),
// and not the statistics SQL Server creates on its own (object_type 21587,
// STATISTICS). All three are intrinsic fields, so they lead the WHERE.
var objectEventPredicates = []string{"ddl_phase = 1", "database_id <> 2", "object_type <> 21587"}

func isObjectEvent(name string) bool { return slices.Contains(ObjectEvents, name) }
