package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Fingerprint struct {
	Root string
	SHA  string
}

// WorktreeFingerprint hashes HEAD, refs and Git's changed paths with their modification times,
// sizes and modes. It stats dirty files without reading their contents; ignored files are excluded.
func WorktreeFingerprint(ctx context.Context, directory string) (Fingerprint, error) {
	root, err := run(ctx, command{dir: directory, args: []string{"rev-parse", "--show-toplevel"}})
	if err != nil {
		if strings.Contains(err.Error(), ErrNotRepository.Error()) {
			return Fingerprint{}, fmt.Errorf("%w: %w", ErrNotRepository, err)
		}
		return Fingerprint{}, fmt.Errorf("find Git repository for %q: %w", directory, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Fingerprint{}, err
	}
	status, err := runBytes(ctx, command{dir: root, args: []string{"--no-optional-locks", "status", "--porcelain=v2", "--branch", "--no-ahead-behind", "--untracked-files=all", "--ignore-submodules=none", "-z"}})
	if err != nil {
		return Fingerprint{}, err
	}
	refs, err := run(ctx, command{dir: root, args: []string{"for-each-ref", "--format=%(objectname) %(refname)"}})
	if err != nil {
		return Fingerprint{}, err
	}
	parts := []string{"worktree-fingerprint-v1", root, string(status), refs}
	paths, err := fingerprintPaths(status)
	if err != nil {
		return Fingerprint{}, err
	}
	for _, path := range paths {
		metadata, err := fingerprintFile(ctx, filepath.Join(root, path))
		if err != nil {
			return Fingerprint{}, err
		}
		parts = append(parts, path, metadata)
	}
	var encoded strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&encoded, "%d:%s", len(part), part)
	}
	digest := sha256.Sum256([]byte(encoded.String()))
	return Fingerprint{Root: root, SHA: hex.EncodeToString(digest[:])}, nil
}

func fingerprintPaths(status []byte) ([]string, error) {
	var paths []string
	entries := strings.Split(string(status), "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if entry == "" || strings.HasPrefix(entry, "# ") {
			continue
		}
		fields := 0
		switch entry[0] {
		case '1':
			fields = 9
		case '2':
			fields = 10
			i++
			if i >= len(entries) || entries[i] == "" {
				return nil, fmt.Errorf("git rename record has no original path")
			}
		case 'u':
			fields = 11
		case '?':
			fields = 2
		default:
			return nil, fmt.Errorf("unexpected Git status record %q", entry)
		}
		parts := strings.SplitN(entry, " ", fields)
		if len(parts) != fields || !filepath.IsLocal(parts[fields-1]) {
			return nil, fmt.Errorf("invalid Git status record %q", entry)
		}
		paths = append(paths, parts[fields-1])
	}
	return paths, nil
}

func fingerprintFile(ctx context.Context, path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "deleted", nil
	}
	if err != nil {
		return "", fmt.Errorf("stat dirty path %s: %w", path, err)
	}
	metadata := fmt.Sprintf("%d:%d:%d", info.ModTime().UnixNano(), info.Size(), info.Mode())
	if info.IsDir() {
		nested, err := WorktreeFingerprint(ctx, path)
		if err != nil {
			return "", err
		}
		metadata += ":" + nested.SHA
	} else if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		metadata += ":" + target
	}
	return metadata, nil
}
