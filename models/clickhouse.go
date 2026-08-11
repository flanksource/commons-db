package models

const (
	ClickHousePropertyMaxExecutionTime    = "max_execution_time"
	ClickHousePropertyMaxRowsToRead       = "max_rows_to_read"
	ClickHousePropertyMaxBytesToRead      = "max_bytes_to_read"
	ClickHousePropertyMaxMemoryUsage      = "max_memory_usage"
	ClickHousePropertyMaxThreads          = "max_threads"
	ClickHousePropertyReadOverflowMode    = "read_overflow_mode"
	ClickHousePropertyTimeoutOverflowMode = "timeout_overflow_mode"

	ClickHouseDefaultMaxExecutionTime    = "10"
	ClickHouseDefaultMaxRowsToRead       = "1000000"
	ClickHouseDefaultMaxBytesToRead      = "268435456"
	ClickHouseDefaultMaxMemoryUsage      = "536870912"
	ClickHouseDefaultMaxThreads          = "4"
	ClickHouseDefaultReadOverflowMode    = "throw"
	ClickHouseDefaultTimeoutOverflowMode = "throw"
)
