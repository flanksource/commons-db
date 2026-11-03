package git

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	gitcommand "github.com/flanksource/commons-db/git/internal/command"
	"github.com/flanksource/commons-db/git/readcache"
)

const (
	minGitMajor = 2
	minGitMinor = 42 // bare blame with supplied contents
)

var (
	gitVersionPattern = regexp.MustCompile(`git version (\d+)\.(\d+)`)
	gitCheck          = &readcache.Cache{Store: &readcache.Memory{}}
)

// command is one git invocation. args never carry credentials: those travel
// in env (see commandEnv).
type command struct {
	dir   string
	env   []string
	stdin []byte
	args  []string
	// progress, when set, receives the progress meter git writes to stderr;
	// the remaining stderr still annotates a failure.
	progress func(FetchProgress)
}

// requireGit retains successful checks for the active executable search path.
func requireGit(ctx context.Context) error {
	_, err := gitCheck.Load(readcache.WithMetadata(ctx, readcache.Metadata{Operation: "version"}), os.Getenv("PATH"), 24*time.Hour, func(ctx context.Context) ([]byte, error) {
		result, err := gitcommand.Run(ctx, gitcommand.Options{Args: []string{"version"}})
		if err != nil {
			return nil, fmt.Errorf("git binary >= %d.%d is required: %w", minGitMajor, minGitMinor, err)
		}
		match := gitVersionPattern.FindStringSubmatch(string(result.Output))
		if match == nil {
			return nil, fmt.Errorf("cannot read git version from %q", strings.TrimSpace(string(result.Output)))
		}
		major, _ := strconv.Atoi(match[1])
		minor, _ := strconv.Atoi(match[2])
		if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
			return nil, fmt.Errorf("git %d.%d is too old: >= %d.%d is required", major, minor, minGitMajor, minGitMinor)
		}
		return result.Output, nil
	})
	return err
}

// run executes git and returns its trimmed stdout. A failure names the
// subcommand and carries git's stderr with URL user info redacted.
func run(ctx context.Context, c command) (string, error) {
	out, err := runBytes(ctx, c)
	// Only trailing whitespace is trimmed: porcelain status lines start with a
	// significant space.
	return strings.TrimRight(string(out), " \r\n"), err
}

// runBytes executes git like run and returns its stdout untouched, for blob
// content whose trailing whitespace is significant.
func runBytes(ctx context.Context, c command) (out []byte, err error) {
	var result gitcommand.Result
	if err := requireGit(ctx); err != nil {
		return nil, err
	}
	options := gitcommand.Options{Directory: c.dir, Environment: c.env, Input: c.stdin, Args: c.args}
	var meter *progressWriter
	if c.progress != nil {
		meter = &progressWriter{onProgress: c.progress}
		options.Stderr = meter
	}
	result, err = gitcommand.Run(ctx, options)
	if meter != nil {
		meter.flush()
		result.Stderr = meter.other.String()
	}
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(c.args, " "), err, Redact(strings.TrimSpace(result.Stderr)))
	}
	return result.Output, nil
}

// startGitTiming returns the finish func of one git operation. A subprocess
// passes its subcommand and finished state, so its CPU and block IO land on
// the "git" metric and on a per-subcommand "git_<subcommand>" metric; go-git
// operations pass "" and nil.
func startGitTiming(ctx context.Context, operation string) func(err error, subcommand string, state *os.ProcessState) {
	return gitcommand.StartTiming(ctx, operation)
}

// baseEnv is the process environment minus inherited git steering, with
// prompts disabled, messages in a stable locale, no system or global config
// file, and isolatedConfig plus pairs as GIT_CONFIG_COUNT entries.
func baseEnv(pairs ...configPair) []string {
	config := make([]gitcommand.Config, len(pairs))
	for index, pair := range pairs {
		config[index] = gitcommand.Config{Key: pair.key, Value: pair.value}
	}
	return gitcommand.Environment(config...)
}
