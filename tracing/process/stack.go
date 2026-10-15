package process

import (
	"bytes"
	"fmt"
	"os"
	"runtime/pprof"
)

// hostStack dumps the goroutines of the host, the one process whose stacks a
// capture can read from inside it.
func hostStack(pid int) (string, error) {
	if pid != os.Getpid() {
		return "", fmt.Errorf("stacks can only be dumped for the host process %d, not %d", os.Getpid(), pid)
	}
	var dump bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&dump, 2); err != nil {
		return "", fmt.Errorf("dump goroutines: %w", err)
	}
	return dump.String(), nil
}
