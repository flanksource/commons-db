package query

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveQueryFile reads p.QueryFile, relative to baseDir, into p.Query.
//
// baseDir is the directory the profile itself lives in: the store's directory
// for a stored profile, a caller's choice for an inline one. The path must stay
// inside it, so a profile cannot name a file anywhere on the host. A profile
// without QueryFile is returned unchanged.
func (p Profile) ResolveQueryFile(baseDir string) (Profile, error) {
	if p.QueryFile == "" || p.QueryDir != "" {
		return p, nil
	}
	if strings.TrimSpace(p.Query) != "" {
		return Profile{}, fmt.Errorf("profile %q sets both query and queryFile; they are mutually exclusive", p.Name)
	}
	if baseDir == "" {
		return Profile{}, fmt.Errorf("profile %q: queryFile %q needs the directory it is relative to", p.Name, p.QueryFile)
	}
	if !filepath.IsLocal(p.QueryFile) {
		return Profile{}, fmt.Errorf("profile %q: queryFile %q must be a relative path inside the profile's directory", p.Name, p.QueryFile)
	}
	dir, err := filepath.Abs(baseDir)
	if err != nil {
		return Profile{}, fmt.Errorf("profile %q: resolve queryFile directory %q: %w", p.Name, baseDir, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, p.QueryFile))
	if err != nil {
		return Profile{}, fmt.Errorf("profile %q: read queryFile: %w", p.Name, err)
	}
	if strings.TrimSpace(string(body)) == "" {
		return Profile{}, fmt.Errorf("profile %q: queryFile %q is empty", p.Name, p.QueryFile)
	}
	p.Query, p.QueryDir = string(body), dir
	return p, nil
}

// Authored is p as it was written: a query read from QueryFile is dropped
// again, so saving the profile keeps the file reference rather than inlining
// the file.
func (p Profile) Authored() Profile {
	if p.QueryFile != "" && p.QueryDir != "" {
		p.Query, p.QueryDir = "", ""
	}
	return p
}

// validateQueryFile refuses a query and a queryFile authored together, and a
// queryFile nothing has read yet: running the empty query in its place would
// be running something other than what the profile names.
func (p Profile) validateQueryFile() error {
	if p.QueryFile == "" {
		return nil
	}
	if p.QueryDir != "" {
		return nil
	}
	if strings.TrimSpace(p.Query) != "" {
		return fmt.Errorf("profile %q sets both query and queryFile; they are mutually exclusive", p.Name)
	}
	return fmt.Errorf("profile %q: queryFile %q has not been read; load the profile from a store or call ResolveQueryFile with its directory",
		p.Name, p.QueryFile)
}
