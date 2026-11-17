package commands

import (
	"errors"
	"fmt"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons/har"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/properties"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Root is the query command tree. Execute also writes the --har archive, so a
// failing command still leaves the requests that explain the failure.
type Root struct {
	*cobra.Command
	capture *httpCapture
}

func (r *Root) Execute() error {
	err := r.Command.Execute()
	return errors.Join(err, r.capture.write())
}

// httpCapture arms every provider and projection request of one command with a
// shared HAR collector through the command context, the seam the query layer's
// observability transport reads.
type httpCapture struct {
	path      string
	level     string
	collector *har.Collector
}

func bindCaptureFlags(root *cobra.Command) *httpCapture {
	capture := &httpCapture{}
	flags := root.PersistentFlags()
	properties.BindFlags[pflag.Value](flags)
	flags.StringVar(&capture.path, "har", "", "Write every provider and projection HTTP request to a HAR file at this path")
	flags.StringVar(&capture.level, "har-level", "full", "HAR capture level: metadata (headers and timings) or full (with JSON bodies)")
	return capture
}

func (c *httpCapture) arm(cmd *cobra.Command) error {
	if strings.TrimSpace(c.path) == "" {
		return nil
	}
	var level logger.LogLevel
	switch c.level {
	case "metadata":
		level = logger.Debug
	case "full":
		level = logger.Trace
	default:
		return fmt.Errorf("--har-level %q is invalid (want metadata or full)", c.level)
	}
	ctx := dbcontext.NewContext(cmd.Context())
	c.collector = har.NewCollector(ctx.HARConfig("http"))
	cmd.SetContext(ctx.WithHARCollector(c.collector).WithRequestHARLevel(level))
	return nil
}

func (c *httpCapture) write() error {
	if c == nil || c.collector == nil {
		return nil
	}
	if err := har.WriteFile(c.collector, c.path); err != nil {
		return fmt.Errorf("write --har %s: %w", c.path, err)
	}
	return nil
}
