package command

import (
	"context"
	"os"
	"strings"
	"time"

	rpchttp "github.com/flanksource/clicky/rpc/http"
)

func StartTiming(ctx context.Context, operation string) func(error, string, *os.ProcessState) {
	started := time.Now()
	return func(err error, subcommand string, state *os.ProcessState) {
		elapsed := time.Since(started)
		failed := int64(0)
		if err != nil {
			failed = 1
		}
		usage := rusageCounters(state)
		counters := append([]rpchttp.TimingCounter{{Name: "ops", Value: 1}, {Name: operation, Value: 1}, {Name: "errors", Value: failed}}, usage...)
		rpchttp.AddTiming(ctx, rpchttp.TimingMetric{Name: "git", Duration: elapsed, Counters: counters})
		if subcommand != "" {
			rpchttp.AddTiming(ctx, rpchttp.TimingMetric{Name: "git_" + strings.ReplaceAll(subcommand, "-", "_"), Duration: elapsed, Counters: append([]rpchttp.TimingCounter{{Name: "ops", Value: 1}, {Name: "errors", Value: failed}}, usage...)})
		}
	}
}

func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-c" || arg == "-C":
			i++
		case !strings.HasPrefix(arg, "-"):
			return arg
		}
	}
	return ""
}
