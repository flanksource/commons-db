package stacks

import (
	"fmt"
	"io"
	"iter"
	"time"

	"github.com/flanksource/commons-db/tracing"
)

// Text is the format a window downloads in as the dumps it holds, each
// checkpoint's in turn.
var Text = tracing.ExportFormat{Name: "text", Ext: "txt", MIME: "text/plain"}

// Summarize is a window as its checkpoints, the goroutines they dumped, and
// those goroutines by state.
func (stacks) Summarize(rows iter.Seq[Stack]) (map[string]any, error) {
	checkpoints := map[string]bool{}
	goroutines := 0
	byState := map[string]int{}
	for record := range rows {
		checkpoints[record.CheckpointID] = true
		if record.Format == FormatGo && record.Error == "" {
			goroutines++
			byState[record.State]++
		}
	}
	return map[string]any{"checkpoints": len(checkpoints), "goroutines": goroutines, "byState": byState}, nil
}

func (stacks) Formats() []tracing.ExportFormat { return []tracing.ExportFormat{Text} }

// Export writes each checkpoint's dump as it was taken, under a line naming
// the checkpoint, the process and why it was dumped; a failed dump is its
// error.
func (stacks) Export(format string, rows iter.Seq[Stack], w io.Writer) error {
	if format != Text.Name {
		return fmt.Errorf("%w %q for trace kind %s", tracing.ErrUnknownFormat, format, Kind)
	}
	checkpoint := ""
	for record := range rows {
		if record.CheckpointID != checkpoint {
			checkpoint = record.CheckpointID
			if _, err := fmt.Fprintf(w, "# checkpoint %s %s pid=%d key=%s transport=%s reason=%s\n\n",
				checkpoint, record.TakenAt.Format(time.RFC3339Nano), record.PID, record.Key, record.Transport, record.Reason); err != nil {
				return err
			}
		}
		text := record.Raw
		if record.Error != "" {
			text = "# error: " + record.Error + "\n" + record.Raw
		}
		if _, err := fmt.Fprintf(w, "%s\n\n", text); err != nil {
			return err
		}
	}
	return nil
}
