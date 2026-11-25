package lease

// DiscardSQLOwnership drops the SQL Server session behind an ownership without unlocking it.
func DiscardSQLOwnership(held Ownership) error {
	return discardSQLConnection(held.(*sqlOwnership).conn)
}
