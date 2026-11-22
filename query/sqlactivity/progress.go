package sqlactivity

import (
	"fmt"
	"strings"
)

// SQLServerProgress holds request and transaction measurements available only in extended SQL Server samples.
type SQLServerProgress struct {
	PercentComplete             *float64 `json:"percentComplete,omitempty" pretty:"label=Progress,unit=percent" filter:"range"`
	EstimatedRemainingMs        *float64 `json:"estimatedRemainingMs,omitempty" pretty:"label=Estimated remaining,type=duration,unit=ms" filter:"duration"`
	TransactionID               string   `json:"transactionId,omitempty" pretty:"label=Transaction ID" filter:"exact"`
	TransactionState            string   `json:"transactionState,omitempty" pretty:"label=Transaction state" filter:"terms"`
	RowsReturned                *int64   `json:"rowsReturned,omitempty" pretty:"label=Rows returned" filter:"range"`
	LastWaitType                string   `json:"lastWaitType,omitempty" pretty:"label=Last wait" filter:"terms"`
	TransactionLogRecords       *int64   `json:"transactionLogRecords,omitempty" pretty:"label=Transaction log records" filter:"range"`
	TransactionLogBytesUsed     *int64   `json:"transactionLogBytesUsed,omitempty" pretty:"label=Transaction log used,type=bytes" filter:"range"`
	TransactionLogBytesReserved *int64   `json:"transactionLogBytesReserved,omitempty" pretty:"label=Transaction log reserved,type=bytes" filter:"range"`
	WaitingTaskCount            *int64   `json:"waitingTaskCount,omitempty" pretty:"label=Waiting tasks" filter:"range"`
	LongestTaskWaitType         string   `json:"longestTaskWaitType,omitempty" pretty:"label=Longest task wait type" filter:"terms"`
	LongestTaskWaitMs           *float64 `json:"longestTaskWaitMs,omitempty" pretty:"label=Longest task wait,type=duration,unit=ms" filter:"duration"`
}

func (p *SQLServerProgress) normalize(command, state string) {
	if p.PercentComplete != nil {
		percent := *p.PercentComplete
		if percent < 0 || percent > 100 || (percent == 0 && !reportsSQLServerProgress(command, state)) {
			p.PercentComplete = nil
		}
	}
	if p.EstimatedRemainingMs != nil && (p.PercentComplete == nil || *p.EstimatedRemainingMs <= 0) {
		p.EstimatedRemainingMs = nil
	}
}

func reportsSQLServerProgress(command, state string) bool {
	switch strings.ToUpper(command) {
	case "ROLLBACK", "KILLED/ROLLBACK", "ALTER INDEX REORGANIZE", "AUTO_SHRINK", "BACKUP DATABASE", "BACKUP LOG",
		"DBCC CHECKDB", "DBCC CHECKFILEGROUP", "DBCC CHECKTABLE", "DBCC INDEXDEFRAG", "DBCC SHRINKDATABASE", "DBCC SHRINKFILE",
		"RECOVERY", "RESTORE DATABASE", "RESTORE LOG", "TDE ENCRYPTION":
		return true
	default:
		return strings.EqualFold(state, "rollback")
	}
}

func sqlServerTransactionState(state int64) string {
	states := [...]string{"initializing", "initialized", "active", "ended", "committing", "prepared", "committed", "rolling back", "rolled back"}
	if state < 0 || state >= int64(len(states)) {
		return fmt.Sprintf("unknown (%d)", state)
	}
	return states[state]
}
