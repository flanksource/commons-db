package sqltext

// StatementType is the coarse classification of a captured statement. It is
// derived from the top-level keywords of the merged SQL — NOT a substring scan —
// so the SQL text never has to be string-matched by callers filtering a trace.
// A statement opening with CREATE/ALTER/DROP/TRUNCATE is StmtDDL; otherwise the
// first top-level DML verb decides, and anything with none is StmtOther.
type StatementType string
