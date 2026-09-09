package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrRendererUnavailable is returned when no facet server is configured and the
// facet binary is not installed. It is a distinct error so a deployment missing
// its renderer fails with an install hint rather than opaquely on every run.
var ErrRendererUnavailable = errors.New("facet renderer unavailable")

// defaultCLITimeout bounds a local render. A cold one pays a pnpm install, so
// this is generous on purpose.
const defaultCLITimeout = 5 * time.Minute

// RenderCLI renders through the local facet binary. It is the fallback for when
// no server is configured; prefer RenderHTTP, which does not pay the cold-start
// cost on every run.
func RenderCLI(
	ctx context.Context,
	data any,
	format, srcDir, entryFile string,
	timeout time.Duration,
) ([]byte, error) {
	binary, err := exec.LookPath("facet")
	if err != nil {
		return nil, fmt.Errorf(
			"%w: install it with `npm install -g @flanksource/facet-cli`, or configure a facet server: %w",
			ErrRendererUnavailable, err)
	}
	if timeout <= 0 {
		timeout = defaultCLITimeout
	}

	dataFile, err := os.CreateTemp("", "facet-data-*.json")
	if err != nil {
		return nil, fmt.Errorf("create data file: %w", err)
	}
	defer os.Remove(dataFile.Name())

	if err := json.NewEncoder(dataFile).Encode(data); err != nil {
		dataFile.Close()
		return nil, fmt.Errorf("write data file: %w", err)
	}
	if err := dataFile.Close(); err != nil {
		return nil, fmt.Errorf("close data file: %w", err)
	}

	outputFile := filepath.Join(os.TempDir(), fmt.Sprintf("facet-out-%d.%s", time.Now().UnixNano(), format))
	defer os.Remove(outputFile)

	renderCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(renderCtx, binary, format, entryFile, "-d", dataFile.Name(), "-o", outputFile)
	command.Dir = srcDir

	output, err := command.CombinedOutput()
	if err != nil {
		if errors.Is(renderCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("facet render timed out after %s: %s", timeout, strings.TrimSpace(string(output)))
		}
		// facet's own output is the only thing that says why a render failed.
		return nil, fmt.Errorf("facet render failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	rendered, err := os.ReadFile(outputFile)
	if err != nil {
		return nil, fmt.Errorf("read rendered output: %w", err)
	}
	return rendered, nil
}

// Render sends the render to the configured server, falling back to the local
// binary only when none is configured.
func Render(
	ctx context.Context,
	server Server,
	data any,
	format, srcDir, entryFile string,
) ([]byte, error) {
	if server.Configured() {
		return RenderHTTP(ctx, server, data, format, srcDir, entryFile, RenderOptions{
			TimestampURL: server.TimestampURL,
			Timeout:      server.Timeout,
		})
	}
	return RenderCLI(ctx, data, format, srcDir, entryFile, server.Timeout)
}
