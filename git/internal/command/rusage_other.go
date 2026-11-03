//go:build !unix

package command

import (
	"os"

	rpchttp "github.com/flanksource/clicky/rpc/http"
)

func rusageCounters(*os.ProcessState) []rpchttp.TimingCounter { return nil }
