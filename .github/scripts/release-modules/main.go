package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

type moduleSpec struct {
	file         string
	module       string
	dependencies []string
}

type releaseOptions struct {
	Version string
	Check   bool
}

var modules = []moduleSpec{
	{
		file:         "recordstore/go.mod",
		module:       "github.com/flanksource/commons-db/recordstore",
		dependencies: []string{"github.com/flanksource/commons-db"},
	},
	{
		file:   "tracing/go.mod",
		module: "github.com/flanksource/commons-db/tracing",
		dependencies: []string{
			"github.com/flanksource/commons-db",
			"github.com/flanksource/commons-db/recordstore",
		},
	},
	{
		file:   "cmd/query/go.mod",
		module: "github.com/flanksource/commons-db/cmd/query",
		dependencies: []string{
			"github.com/flanksource/commons-db",
			"github.com/flanksource/commons-db/recordstore",
			"github.com/flanksource/commons-db/tracing",
		},
	},
}

func rewriteModuleRequirements(content []byte, version string, module string, dependencies []string) ([]byte, error) {
	if !semver.IsValid(version) {
		return nil, fmt.Errorf("invalid release version %q", version)
	}
	file, err := modfile.Parse(module, content, nil)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", module, err)
	}
	if file.Module == nil || file.Module.Mod.Path != module {
		return nil, fmt.Errorf("expected module %s", module)
	}
	for _, dependency := range dependencies {
		found := false
		for _, requirement := range file.Require {
			if requirement.Mod.Path == dependency {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("module %s is missing requirement %s", module, dependency)
		}
		if err := file.AddRequire(dependency, version); err != nil {
			return nil, fmt.Errorf("set %s requirement in %s: %w", dependency, module, err)
		}
	}
	return file.Format()
}

func prepareRelease(options releaseOptions) error {
	type update struct {
		path    string
		content []byte
		mode    os.FileMode
	}
	updates := make([]update, 0, len(modules))
	for _, module := range modules {
		original, err := os.ReadFile(module.file)
		if err != nil {
			return fmt.Errorf("read %s: %w", module.file, err)
		}
		content, err := rewriteModuleRequirements(original, options.Version, module.module, module.dependencies)
		if err != nil {
			return fmt.Errorf("prepare %s: %w", module.file, err)
		}
		if options.Check {
			if !bytes.Equal(original, content) {
				return fmt.Errorf("requirements in %s are not aligned with release %s", module.file, options.Version)
			}
			continue
		}
		info, err := os.Stat(module.file)
		if err != nil {
			return fmt.Errorf("stat %s: %w", module.file, err)
		}
		updates = append(updates, update{module.file, content, info.Mode().Perm()})
	}
	for _, update := range updates {
		if err := os.WriteFile(update.path, update.content, update.mode); err != nil {
			return fmt.Errorf("write %s: %w", update.path, err)
		}
	}
	return nil
}

func main() {
	check := flag.Bool("check", false, "verify sibling requirements without modifying files")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: release-modules [--check] vX.Y.Z")
		os.Exit(2)
	}
	if err := prepareRelease(releaseOptions{Version: flag.Arg(0), Check: *check}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
