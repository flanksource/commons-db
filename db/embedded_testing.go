package db

import "fmt"

func fastTestingStartParameters(enabled bool) map[string]string {
	if !enabled {
		return nil
	}
	return map[string]string{
		"fsync":              "off",
		"synchronous_commit": "off",
		"full_page_writes":   "off",
		"max_connections":    "200",
	}
}

func validateFastTestingSettings(fsync, synchronousCommit, fullPageWrites string, maxConnections int) error {
	if fsync != "off" {
		return fmt.Errorf("fast embedded PostgreSQL requires fsync=off, got %q", fsync)
	}
	if synchronousCommit != "off" {
		return fmt.Errorf("fast embedded PostgreSQL requires synchronous_commit=off, got %q", synchronousCommit)
	}
	if fullPageWrites != "off" {
		return fmt.Errorf("fast embedded PostgreSQL requires full_page_writes=off, got %q", fullPageWrites)
	}
	if maxConnections < 200 {
		return fmt.Errorf("fast embedded PostgreSQL requires max_connections >= 200, got %d", maxConnections)
	}
	return nil
}
