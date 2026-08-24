package app

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Vite dev command", func() {
	It("configures the Vite port and Query API URL", func() {
		webDir := filepath.Join("query", "www")
		cmd := viteDevCommand(context.Background(), viteDevCommandOptions{
			WebDir: webDir, VitePort: 43210, APIHost: "127.0.0.1", APIPort: 8080,
		})

		Expect(cmd.Dir).To(Equal(webDir))
		Expect(cmd.Args).To(Equal([]string{
			"pnpm", "exec", "vite",
			"--strictPort", "--host", "127.0.0.1", "--port", "43210",
		}))
		Expect(cmd.Env).To(ContainElement("QUERY_API_URL=http://127.0.0.1:8080"))
	})

	It("resolves the web app from supported working directories", func() {
		queryModuleDir, err := filepath.Abs(filepath.Join("..", ".."))
		Expect(err).NotTo(HaveOccurred())
		repositoryRoot := filepath.Clean(filepath.Join(queryModuleDir, "..", ".."))
		expected := filepath.Join(queryModuleDir, "www")

		for name, workingDir := range map[string]string{
			"query module":    queryModuleDir,
			"repository root": repositoryRoot,
		} {
			By(name)
			actual, err := resolveViteWebDir(workingDir)

			Expect(err).NotTo(HaveOccurred())
			Expect(actual).To(Equal(expected))
		}
	})

	It("rejects a working directory outside the Query source roots", func() {
		workingDir, err := filepath.Abs(".")
		Expect(err).NotTo(HaveOccurred())

		_, err = resolveViteWebDir(workingDir)

		Expect(err).To(MatchError(ContainSubstring("expected cmd/query/www/package.json or www/package.json")))
	})
})
