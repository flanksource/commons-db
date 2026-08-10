package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestViteDevCommandRunsFromTheQueryModule(t *testing.T) {
	cmd := viteDevCommand(context.Background(), 43210, "127.0.0.1", 8080)

	require.Empty(t, cmd.Dir)
	require.Equal(t, []string{
		"pnpm", "--dir", "www", "exec", "vite",
		"--strictPort", "--host", "127.0.0.1", "--port", "43210",
	}, cmd.Args)
	require.Contains(t, cmd.Env, "QUERY_API_URL=http://127.0.0.1:8080")
}
