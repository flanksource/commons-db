package git

import (
	"context"
	"fmt"
	"strings"
)

type IndexFilesOptions struct {
	Directory        string
	IncludeUntracked bool
}

func IndexFiles(ctx context.Context, options IndexFilesOptions) ([]string, error) {
	found, err := Discover(ctx, DiscoverOptions{Path: options.Directory})
	if err != nil {
		return nil, err
	}
	if found.Bare {
		return nil, fmt.Errorf("index listing requires a working checkout")
	}
	args := []string{"ls-files", "-z", "--cached"}
	if options.IncludeUntracked {
		args = append(args, "--others", "--exclude-standard")
	}
	raw, err := runBytes(ctx, command{dir: found.Root, args: args})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return []string{}, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, fmt.Errorf("unterminated Git index listing")
	}
	return strings.Split(string(raw[:len(raw)-1]), "\x00"), nil
}
