//go:build unix

package git

import (
	"os"
	"syscall"

	rpchttp "github.com/flanksource/clicky/rpc/http"
)

// rusageCounters is the finished child's CPU, block IO and peak RSS (a max
// counter, so a metric reports its largest child).
func rusageCounters(state *os.ProcessState) []rpchttp.TimingCounter {
	if state == nil {
		return nil
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok {
		return nil
	}
	return []rpchttp.TimingCounter{
		{Name: "cpu_user_us", Value: usage.Utime.Nano() / 1000},
		{Name: "cpu_sys_us", Value: usage.Stime.Nano() / 1000},
		{Name: "in_blocks", Value: int64(usage.Inblock)},
		{Name: "out_blocks", Value: int64(usage.Oublock)},
		{Name: "max_rss_bytes", Value: rpchttp.MaxRSSBytes(int64(usage.Maxrss)), Aggregate: rpchttp.AggregateMax},
	}
}
