package main

import (
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

func prepareRelease(version string) error {
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
		content, err := rewriteModuleRequirements(original, version, module.module, module.dependencies)
		if err != nil {
			return fmt.Errorf("prepare %s: %w", module.file, err)
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
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: release-modules vX.Y.Z")
		os.Exit(2)
	}
	if err := prepareRelease(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
