package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gitcommand "github.com/flanksource/commons-db/git/internal/command"
	"github.com/flanksource/commons-db/git/readcache"
)

type DiscoverOptions struct{ Path string }

type Discovery struct {
	Root            string
	GitDirectory    string
	CommonDirectory string
	Bare            bool
}

var discoveryCache = readcache.Cache{Store: &readcache.Memory{}}

// Discover locates a repository without resolving a revision, including unborn checkouts.
func Discover(ctx context.Context, options DiscoverOptions) (Discovery, error) {
	if options.Path == "" {
		return Discovery{}, fmt.Errorf("discover git: path is required")
	}
	path, err := filepath.Abs(options.Path)
	if err != nil {
		return Discovery{}, err
	}
	raw, err := discoveryCache.Load(readcache.WithMetadata(ctx, readcache.Metadata{Operation: "discover", Repository: path}), path, 30*time.Second, func(ctx context.Context) ([]byte, error) {
		found, err := discover(ctx, DiscoverOptions{Path: path})
		if err != nil {
			return nil, err
		}
		return json.Marshal(found)
	})
	if err != nil {
		return Discovery{}, err
	}
	var found Discovery
	err = json.Unmarshal(raw, &found)
	return found, err
}

func discover(ctx context.Context, options DiscoverOptions) (Discovery, error) {
	if err := ctx.Err(); err != nil {
		return Discovery{}, err
	}
	if options.Path == "" {
		return Discovery{}, fmt.Errorf("discover git: path is required")
	}
	directory, err := filepath.Abs(options.Path)
	if err != nil {
		return Discovery{}, err
	}
	for {
		info, err := os.Stat(directory)
		if err == nil {
			if !info.IsDir() {
				directory = filepath.Dir(directory)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Discovery{}, err
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return Discovery{}, err
		}
		directory = parent
	}
	result, err := gitcommand.Run(ctx, gitcommand.Options{Directory: directory, Args: []string{"rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--is-bare-repository"}})
	if err != nil {
		if ctx.Err() != nil {
			return Discovery{}, ctx.Err()
		}
		if strings.Contains(result.Stderr, "not a git repository") {
			return Discovery{}, fmt.Errorf("%w: %s", ErrNotRepository, options.Path)
		}
		return Discovery{}, fmt.Errorf("discover git %q: %w: %s", options.Path, err, result.Stderr)
	}
	parts := strings.Split(strings.TrimSpace(string(result.Output)), "\n")
	if len(parts) != 3 {
		return Discovery{}, fmt.Errorf("discover git: unexpected result %q", result.Output)
	}
	found := Discovery{GitDirectory: parts[0], CommonDirectory: parts[1], Bare: parts[2] == "true", Root: parts[0]}
	if !found.Bare {
		found.Root, err = run(ctx, command{dir: directory, args: []string{"rev-parse", "--show-toplevel"}})
		if err != nil {
			return Discovery{}, err
		}
	}
	found.Root, err = filepath.EvalSymlinks(found.Root)
	return found, err
}

// Head returns the full commit ID of a local repository's HEAD.
func Head(ctx context.Context, directory string) (string, error) {
	return run(ctx, command{dir: directory, args: []string{"rev-parse", "--verify", "HEAD^{commit}"}})
}
