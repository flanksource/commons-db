package commands_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/cmd/query/internal/commands"
	"github.com/flanksource/commons/har"
	ginkgo "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var _ = ginkgo.Describe("Projections CLI", func() {
	ginkgo.DescribeTable("explains the relevant projection workflow in command help",
		func(name string, sections []string) {
			args := []string{"projections"}
			if name != "" {
				args = append(args, name)
			}
			var output bytes.Buffer
			root, err := commands.New(commands.Options{Args: append(args, "--help", "--no-color"), Stdout: &output, Stderr: &output, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Execute()).To(Succeed())
			for _, section := range append(sections, "Selecting manifests:", "Output:", "query projections syntax --help") {
				Expect(output.String()).To(ContainSubstring(section))
			}
			Expect(output.String()).NotTo(ContainSubstring("\x1b["))
			Expect(output.String()).NotTo(ContainSubstring("selected Faro context"))
		},
		ginkgo.Entry("root", "", []string{"Workflow:", "Manifest syntax:", "projections.flanksource.com/v1alpha1", "entries: []", "exec, http or profile"}),
		ginkgo.Entry("run", "run", []string{"Source queries:", "Filtering and validation:", "Results and connections:", "perTarget", "source.body", "profile runs a named Query profile", "metrics.notices", "now-24h/h"}),
		ginkgo.Entry("apply", "apply", []string{"Matching and mapping syntax:", "List strategies:", "Creation and aggregation:", "Validation and execution:", "Dry runs and results:", "replaceMatching"}),
		ginkgo.Entry("verify", "verify", []string{"Manifest checks:", "Target checks:", "Results and limits:", "future source rows", "without loading profiles or a database"}),
	)

	ginkgo.It("keeps local exec operations out of the served command tree", func() {
		dir := ginkgo.GinkgoT().TempDir()
		root, err := commands.New(commands.Options{Args: []string{"--config-dir", dir, "--profiles-dir", filepath.Join(dir, "profiles"), "--db", "", "serve"}, Stdout: io.Discard, Stderr: io.Discard, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
		Expect(err).NotTo(HaveOccurred())
		command, _, _ := root.Find([]string{"projections"})
		Expect(command.Name()).NotTo(Equal("projections"))
	})
	for _, plain := range []bool{true, false} {
		ginkgo.It("renders the complete syntax reference with optional color", func() {
			ginkgo.GinkgoT().Setenv("NO_COLOR", "")
			ginkgo.GinkgoT().Setenv("COLOR", "")
			ginkgo.GinkgoT().Setenv("TERM", "xterm-256color")
			ginkgo.GinkgoT().Setenv("CLICKY_FORCE_INTERACTIVE", "1")
			args := []string{"projections", "syntax", "--help"}
			if plain {
				args = append(args, "--no-color")
			}
			var output bytes.Buffer
			root, err := commands.New(commands.Options{Args: args, Stdout: &output, Stderr: &output, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Execute()).To(Succeed())
			Expect(output.String()).To(ContainSubstring("perTarget"))
			Expect(output.String()).To(ContainSubstring("XDG_CONFIG_HOME"))
			Expect(output.String()).To(ContainSubstring("faro projection run"))
			Expect(output.String()).To(ContainSubstring("hasMore"))
			if plain {
				Expect(output.String()).NotTo(ContainSubstring("\x1b["))
			} else {
				Expect(output.String()).To(ContainSubstring("\x1b["))
			}
		})
	}
	ginkgo.It("runs a profile source through the --profiles-dir profile service with rendered params", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"service":"api","env":%q}]`, r.URL.Query().Get("env"))
		}))
		defer server.Close()
		dir := ginkgo.GinkgoT().TempDir()
		profilesDir := filepath.Join(dir, "profiles")
		Expect(os.MkdirAll(profilesDir, 0700)).To(Succeed())
		profile := "profile: env-services\nprovider:\n  type: http\n  options: {url: " + server.URL + "}\nquery: /rows?env={{.params.env}}\nparams:\n  - {name: env, type: string, required: true}\ncolumns:\n  - {name: service, type: string}\n  - {name: env, type: string}\n"
		Expect(os.WriteFile(filepath.Join(profilesDir, "env-services.yaml"), []byte(profile), 0600)).To(Succeed())
		target := filepath.Join(dir, "target.yaml")
		Expect(os.WriteFile(target, []byte("entries: []\n"), 0600)).To(Succeed())
		manifest := filepath.Join(dir, "projections.yaml")
		Expect(os.WriteFile(manifest, []byte("apiVersion: projections.flanksource.com/v1alpha1\nkind: Projection\nmetadata: {name: services}\nspec:\n  source:\n    query:\n      profile:\n        name: env-services\n        all: true\n        params: {env: '{{.context.name}}-eu'}\n  target: {path: target.yaml, select: '$.entries[*]'}\n  match: [source.service == target.id]\n  set:\n    $.id: {value: source.service}\n    $.env: {value: source.env}\n"), 0600)).To(Succeed())
		var output bytes.Buffer
		root, err := commands.New(commands.Options{Args: []string{"--config-dir", dir, "--profiles-dir", profilesDir, "--db=", "projections", "apply", manifest, "--context", "staging", "--no-progress"}, Stdout: &output, Stderr: &output, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Execute()).To(Succeed(), output.String())
		var written struct {
			Entries []map[string]string `json:"entries"`
		}
		body, err := os.ReadFile(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(yaml.Unmarshal(body, &written)).To(Succeed())
		Expect(written.Entries).To(Equal([]map[string]string{{"id": "api", "env": "staging-eu"}}))
	})
	ginkgo.It("renders profile params from nested --context-file values", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"service":"api","env":%q}]`, r.URL.Query().Get("env"))
		}))
		defer server.Close()
		dir := ginkgo.GinkgoT().TempDir()
		profilesDir := filepath.Join(dir, "profiles")
		Expect(os.MkdirAll(profilesDir, 0700)).To(Succeed())
		profile := "profile: env-services\nprovider:\n  type: http\n  options: {url: " + server.URL + "}\nquery: /rows?env={{.params.env}}\nparams:\n  - {name: env, type: string, required: true}\ncolumns:\n  - {name: service, type: string}\n  - {name: env, type: string}\n"
		Expect(os.WriteFile(filepath.Join(profilesDir, "env-services.yaml"), []byte(profile), 0600)).To(Succeed())
		contextFile := filepath.Join(dir, "collection.yaml")
		Expect(os.WriteFile(contextFile, []byte("environment: {region: eu, tier: uat}\n"), 0600)).To(Succeed())
		manifest := filepath.Join(dir, "projections.yaml")
		Expect(os.WriteFile(manifest, []byte("apiVersion: projections.flanksource.com/v1alpha1\nkind: Projection\nmetadata: {name: services}\nspec:\n  source:\n    query:\n      profile:\n        name: env-services\n        all: true\n        params: {env: '{{.context.environment.tier}}-{{.context.environment.region}}'}\n  target: {path: target.yaml, select: '$.entries[*]'}\n  match: [source.service == target.id]\n  set:\n    $.id: {value: source.service}\n    $.env: {value: source.env}\n"), 0600)).To(Succeed())
		target := filepath.Join(dir, "target.yaml")
		Expect(os.WriteFile(target, []byte("entries: []\n"), 0600)).To(Succeed())
		var output bytes.Buffer
		root, err := commands.New(commands.Options{Args: []string{"--config-dir", dir, "--profiles-dir", profilesDir, "--db=", "projections", "apply", manifest, "--context-file", contextFile, "--no-progress"}, Stdout: &output, Stderr: &output, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Execute()).To(Succeed(), output.String())
		var written struct {
			Entries []map[string]string `json:"entries"`
		}
		body, err := os.ReadFile(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(yaml.Unmarshal(body, &written)).To(Succeed())
		Expect(written.Entries).To(Equal([]map[string]string{{"id": "api", "env": "uat-eu"}}))
	})
	for _, outcome := range []struct {
		name   string
		status int
	}{{"succeeds", http.StatusOK}, {"fails", http.StatusInternalServerError}} {
		ginkgo.It("writes every profile request to --har when the command "+outcome.name, func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(outcome.status)
				_, _ = fmt.Fprintf(w, `[{"service":"api","env":%q}]`, r.URL.Query().Get("env"))
			}))
			defer server.Close()
			dir := ginkgo.GinkgoT().TempDir()
			profilesDir := filepath.Join(dir, "profiles")
			Expect(os.MkdirAll(profilesDir, 0700)).To(Succeed())
			profile := "profile: env-services\nprovider:\n  type: http\n  options: {url: " + server.URL + "}\nquery: /rows?env={{.params.env}}\nparams:\n  - {name: env, type: string, required: true}\ncolumns:\n  - {name: service, type: string}\n  - {name: env, type: string}\n"
			Expect(os.WriteFile(filepath.Join(profilesDir, "env-services.yaml"), []byte(profile), 0600)).To(Succeed())
			manifest := filepath.Join(dir, "projections.yaml")
			Expect(os.WriteFile(manifest, []byte("apiVersion: projections.flanksource.com/v1alpha1\nkind: Projection\nmetadata: {name: services}\nspec:\n  source:\n    query:\n      profile:\n        name: env-services\n        params: {env: '{{.context.name}}'}\n"), 0600)).To(Succeed())
			archive := filepath.Join(dir, "out.har")
			var output bytes.Buffer
			root, err := commands.New(commands.Options{Args: []string{"--config-dir", dir, "--profiles-dir", profilesDir, "--db=", "--har", archive, "projections", "run", manifest, "--context", "uat", "--no-progress"}, Stdout: &output, Stderr: &output, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
			Expect(err).NotTo(HaveOccurred())
			executeErr := root.Execute()
			if outcome.status == http.StatusOK {
				Expect(executeErr).NotTo(HaveOccurred(), output.String())
			} else {
				Expect(executeErr).To(HaveOccurred())
			}

			var written har.File
			body, err := os.ReadFile(archive)
			Expect(err).NotTo(HaveOccurred())
			Expect(json.Unmarshal(body, &written)).To(Succeed())
			Expect(written.Log.Entries).NotTo(BeEmpty())
			Expect(written.Log.Entries[0].Request.URL).To(Equal(server.URL + "/rows?env=uat"))
			Expect(written.Log.Entries[0].Response.Status).To(Equal(outcome.status))
		})
	}
	ginkgo.It("refuses a context name supplied by both --context and --context-file", func() {
		dir := ginkgo.GinkgoT().TempDir()
		contextFile := filepath.Join(dir, "collection.yaml")
		Expect(os.WriteFile(contextFile, []byte("name: from-file\n"), 0600)).To(Succeed())
		manifest := filepath.Join(dir, "projections.yaml")
		Expect(os.WriteFile(manifest, []byte("apiVersion: projections.flanksource.com/v1alpha1\nkind: Projection\nmetadata: {name: rows}\nspec:\n  source:\n    query:\n      exec:\n        script: echo '[]'\n"), 0600)).To(Succeed())
		var output bytes.Buffer
		root, err := commands.New(commands.Options{Args: []string{"--config-dir", dir, "--db=", "projections", "run", manifest, "--context", "from-flag", "--context-file", contextFile, "--no-progress"}, Stdout: &output, Stderr: &output, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Execute()).To(MatchError(ContainSubstring("context.name is set by both --context and " + contextFile)))
	})

	for _, command := range []string{"verify", "run", "apply", "syntax"} {
		ginkgo.It("provides "+command+" without loading invalid profiles or a database", func() {
			dir := ginkgo.GinkgoT().TempDir()
			profiles := filepath.Join(dir, "profiles")
			Expect(os.MkdirAll(profiles, 0700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(profiles, "broken.yaml"), []byte("[invalid"), 0600)).To(Succeed())
			var output bytes.Buffer
			root, err := commands.New(commands.Options{Args: []string{"--profiles-dir", profiles, "--db", "invalid", "projections", command, "--help"}, Stdout: &output, Stderr: &output, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Execute()).To(Succeed())
			Expect(output.String()).To(ContainSubstring("query projections " + command))
		})
	}
})
