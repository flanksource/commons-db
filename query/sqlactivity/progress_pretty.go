package sqlactivity

import (
	"strconv"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
)

// Detail renders the measurements SQL Server supplied for this request.
func (p SQLServerProgress) Detail() api.Textable {
	fields := make(map[string]any)
	for label, value := range map[string]string{
		"Transaction ID": p.TransactionID, "Transaction state": p.TransactionState,
		"Last wait": p.LastWaitType, "Longest task wait type": p.LongestTaskWaitType,
	} {
		if value != "" {
			fields[label] = value
		}
	}
	for label, value := range map[string]*int64{
		"Rows returned": p.RowsReturned, "Transaction log records": p.TransactionLogRecords, "Waiting tasks": p.WaitingTaskCount,
	} {
		if value != nil {
			fields[label] = api.HumanNumber(*value)
		}
	}
	for label, value := range map[string]*int64{
		"Transaction log used": p.TransactionLogBytesUsed, "Transaction log reserved": p.TransactionLogBytesReserved,
	} {
		if value != nil {
			fields[label] = bytesReading(value)
		}
	}
	if p.PercentComplete != nil {
		fields["Progress"] = progressPercent(*p.PercentComplete)
	}
	if p.EstimatedRemainingMs != nil {
		fields["Estimated remaining"] = durationReading(p.EstimatedRemainingMs)
	}
	if p.LongestTaskWaitMs != nil {
		fields["Longest task wait"] = durationReading(p.LongestTaskWaitMs)
	}
	if len(fields) == 0 {
		return nil
	}
	return clicky.Map(fields)
}

func progressPercent(value float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(value, 'f', 1, 64), ".0") + "%"
}
