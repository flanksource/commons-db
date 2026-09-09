package report

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

//go:embed template/*.tsx template/package.json
var templateFS embed.FS

const templateRoot = "template"

// SourceDir overrides the extracted template with a directory on disk, for
// developing the TSX without rebuilding the binary.
var SourceDir = os.Getenv("QUERY_REPORT_SOURCE_DIR")

var (
	extractOnce sync.Once
	extractDir  string
	extractErr  error
)

// TemplateDir returns a directory holding the report template, extracting the
// embedded copy on first use.
//
// The location is content-addressed by the template's own hash, so the pnpm
// install and Vite build cached under .facet/ survive across renders and across
// restarts — a cold render costs roughly ten times a warm one — while a change
// to the template lands in a different directory and is never served stale.
func TemplateDir() (string, error) {
	if SourceDir != "" {
		return SourceDir, nil
	}
	extractOnce.Do(func() { extractDir, extractErr = extractTemplate() })
	return extractDir, extractErr
}

func extractTemplate() (string, error) {
	files, digest, err := readTemplate()
	if err != nil {
		return "", err
	}

	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	root := filepath.Join(base, "commons-db", "query-report-"+digest[:16])

	// A marker written last means a half-extracted directory from a crashed run
	// is re-extracted rather than handed to facet as if it were complete.
	marker := filepath.Join(root, ".extracted")
	if _, err := os.Stat(marker); err == nil {
		return root, nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create template cache %q: %w", root, err)
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("create %q: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return "", fmt.Errorf("write %q: %w", path, err)
		}
	}
	if err := os.WriteFile(marker, []byte(digest), 0o600); err != nil {
		return "", fmt.Errorf("write extraction marker: %w", err)
	}
	return root, nil
}

// readTemplate reads the embedded template and hashes it. The hash covers both
// names and contents, in a stable order, so any edit changes the digest.
func readTemplate() (map[string][]byte, string, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(templateFS, templateRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(templateRoot, path)
		if err != nil {
			return err
		}
		files[relative] = content
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("read embedded template: %w", err)
	}
	if len(files) == 0 {
		return nil, "", fmt.Errorf("embedded report template is empty")
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	hash := sha256.New()
	for _, name := range names {
		hash.Write([]byte(name))
		hash.Write(files[name])
	}
	return files, hex.EncodeToString(hash.Sum(nil)), nil
}
