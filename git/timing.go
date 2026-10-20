package git

import (
	"context"
	"time"

	rpchttp "github.com/flanksource/clicky/rpc/http"
)

func startGitTiming(ctx context.Context, operation string) func(error) {
	started := time.Now()
	return func(err error) {
		metric := rpchttp.TimingMetric{Name: "git", Duration: time.Since(started), Counters: []rpchttp.TimingCounter{
			{Name: "ops", Value: 1}, {Name: operation, Value: 1}, {Name: "errors"},
		}}
		if err != nil {
			metric.Counters[2].Value = 1
		}
		rpchttp.AddTiming(ctx, metric)
	}
}
