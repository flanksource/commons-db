package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	rpchttp "github.com/flanksource/clicky/rpc/http"
)

const (
	minGitMajor = 2
	minGitMinor = 42 // bare blame with supplied contents
)

var (
	gitVersionPattern = regexp.MustCompile(`git version (\d+)\.(\d+)`)
	gitCheck          struct {
		once sync.Once
		err  error
	}
	// inheritedGitEnv are variables of the calling process that would steer
	// git away from the repository, credentials and config this package sets,
	// or make it print the Authorization header (GIT_TRACE*, GIT_CURL_VERBOSE)
	// into the stderr an error carries.
	inheritedGitEnv = []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_OBJECT_DIRECTORY=",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_COMMON_DIR=", "GIT_CONFIG=", "GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_",
		"GIT_CONFIG_VALUE_", "GIT_CONFIG_PARAMETERS=", "GIT_CONFIG_GLOBAL=", "GIT_CONFIG_SYSTEM=", "GIT_CONFIG_NOSYSTEM=",
		"GIT_SSH=", "GIT_SSH_COMMAND=", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_TERMINAL_PROMPT=", "GIT_AUTHOR_",
		"GIT_COMMITTER_", "GIT_TRACE", "GIT_CURL_VERBOSE=", "GIT_REDACT_COOKIES="}
	// isolatedConfig keeps hooks and commit signing of a user's own config
	// out of every command; the system and global config files are not read
	// at all (baseEnv).
	isolatedConfig = []configPair{{"core.hooksPath", os.DevNull}, {"commit.gpgSign", "false"}, {"tag.gpgSign", "false"}}
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

// requireGit checks once per process that a git binary >= 2.42 is on PATH.
func requireGit(ctx context.Context) error {
	gitCheck.once.Do(func() {
		out, err := exec.CommandContext(ctx, "git", "version").Output()
		if err != nil {
			gitCheck.err = fmt.Errorf("git binary >= %d.%d is required: %w", minGitMajor, minGitMinor, err)
			return
		}
		match := gitVersionPattern.FindStringSubmatch(string(out))
		if match == nil {
			gitCheck.err = fmt.Errorf("cannot read git version from %q", strings.TrimSpace(string(out)))
			return
		}
		major, _ := strconv.Atoi(match[1])
		minor, _ := strconv.Atoi(match[2])
		if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
			gitCheck.err = fmt.Errorf("git %d.%d is too old: >= %d.%d is required", major, minor, minGitMajor, minGitMinor)
		}
	})
	return gitCheck.err
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
	finish := startGitTiming(ctx, "commands")
	cmd := exec.CommandContext(ctx, "git", c.args...)
	defer func() { finish(err, gitSubcommand(c.args), cmd.ProcessState) }()
	if err := requireGit(ctx); err != nil {
		return nil, err
	}
	cmd.Dir = c.dir
	cmd.Env = c.env
	if cmd.Env == nil {
		cmd.Env = baseEnv()
	}
	if c.stdin != nil {
		cmd.Stdin = bytes.NewReader(c.stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	var meter *progressWriter
	if c.progress != nil {
		meter = &progressWriter{onProgress: c.progress}
		cmd.Stderr = meter
	}
	err = cmd.Run()
	if meter != nil {
		meter.flush()
		stderr = meter.other
	}
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(c.args, " "), err, Redact(strings.TrimSpace(stderr.String())))
	}
	return stdout.Bytes(), nil
}

// startGitTiming returns the finish func of one git operation. A subprocess
// passes its subcommand and finished state, so its CPU and block IO land on
// the "git" metric and on a per-subcommand "git_<subcommand>" metric; go-git
// operations pass "" and nil.
func startGitTiming(ctx context.Context, operation string) func(err error, subcommand string, state *os.ProcessState) {
	started := time.Now()
	return func(err error, subcommand string, state *os.ProcessState) {
		elapsed := time.Since(started)
		failed := int64(0)
		if err != nil {
			failed = 1
		}
		usage := rusageCounters(state)
		counters := append([]rpchttp.TimingCounter{{Name: "ops", Value: 1}, {Name: operation, Value: 1}, {Name: "errors", Value: failed}}, usage...)
		rpchttp.AddTiming(ctx, rpchttp.TimingMetric{Name: "git", Duration: elapsed, Counters: counters})
		if subcommand != "" {
			rpchttp.AddTiming(ctx, rpchttp.TimingMetric{Name: "git_" + strings.ReplaceAll(subcommand, "-", "_"), Duration: elapsed,
				Counters: append([]rpchttp.TimingCounter{{Name: "ops", Value: 1}, {Name: "errors", Value: failed}}, usage...)})
		}
	}
}

// gitSubcommand is the first argument after git's global options (-c and -C
// take a value).
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-c" || arg == "-C":
			i++
		case !strings.HasPrefix(arg, "-"):
			return arg
		}
	}
	return ""
}

// baseEnv is the process environment minus inherited git steering, with
// prompts disabled, messages in a stable locale, no system or global config
// file, and isolatedConfig plus pairs as GIT_CONFIG_COUNT entries.
func baseEnv(pairs ...configPair) []string {
	env := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		if !hasAnyPrefix(entry, inheritedGitEnv) && !strings.HasPrefix(entry, "LC_ALL=") {
			env = append(env, entry)
		}
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	pairs = append(slices.Clone(isolatedConfig), pairs...)
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(pairs)))
	for i, pair := range pairs {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, pair.key), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, pair.value))
	}
	return env
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
