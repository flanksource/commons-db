//go:build !unix

package git

import (
	"os"

	rpchttp "github.com/flanksource/clicky/rpc/http"
)

func rusageCounters(*os.ProcessState) []rpchttp.TimingCounter { return nil }
