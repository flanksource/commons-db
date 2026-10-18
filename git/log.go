package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Commit is one commit as Cache.Log reads it. Files are the paths it changed
// against its first parent; a merge commit (len(Parents) > 1) lists none.
type Commit struct {
	SHA      string
	ShortSHA string
	Subject  string
	Body     string
	Author   Signature
	Date     time.Time
	Parents  []string
	Files    []string
}

// IsMerge reports whether the commit has more than one parent.
func (c Commit) IsMerge() bool { return len(c.Parents) > 1 }

// LogOptions configures Cache.Log. Limit > 0 keeps only the newest Limit
// commits; Paths keeps only the commits that touch them.
type LogOptions struct {
	Limit int
	Paths []string
}

// Comparison is how head relates to base: Ahead commits on head not on base,
// Behind commits on base not on head, and their merge base.
type Comparison struct {
	Ahead     int
	Behind    int
	MergeBase string
}

// logRecord prefixes every commit of the log format, so the --name-only
// file list that follows a commit's header belongs to that record.
const logRecord = "\x1e"

var logFormat = "--format=" + logRecord + strings.Join([]string{"%H", "%h", "%P", "%an", "%ae", "%aI", "%s", "%b"}, "%x00") + "%x00"

// Log returns the commits reachable from head and not from base (git log
// base..head), oldest first: parents before children (--topo-order), so a
// merge commit follows every commit it merges. base and head resolve like
// Checkout refs; both are fetched into the mirror first.
func (c *Cache) Log(ctx context.Context, repo *Repository, base, head string, options LogOptions) ([]Commit, error) {
	if options.Limit < 0 {
		return nil, fmt.Errorf("invalid log limit %d", options.Limit)
	}
	args := []string{"-c", "core.quotePath=false", "log", "--topo-order", "--reverse", "--no-renames", "--name-only", logFormat}
	if options.Limit > 0 {
		args = append(args, "--max-count="+strconv.Itoa(options.Limit))
	}
	var out string
	err := c.withRange(ctx, repo, base, head, func(mirror, baseSHA, headSHA string) (err error) {
		args = append(args, "--end-of-options", baseSHA+".."+headSHA, "--")
		out, err = run(ctx, command{dir: mirror, args: append(args, options.Paths...)})
		return err
	})
	if err != nil {
		return nil, err
	}
	return parseLog(out)
}

// Compare counts the commits head is ahead of and behind base and finds
// their merge base; refs without a common ancestor are an error.
func (c *Cache) Compare(ctx context.Context, repo *Repository, base, head string) (Comparison, error) {
	var comparison Comparison
	err := c.withRange(ctx, repo, base, head, func(mirror, baseSHA, headSHA string) error {
		counts, err := run(ctx, command{dir: mirror, args: []string{"rev-list", "--left-right", "--count",
			"--end-of-options", baseSHA + "..." + headSHA}})
		if err != nil {
			return err
		}
		if _, err := fmt.Sscanf(counts, "%d\t%d", &comparison.Behind, &comparison.Ahead); err != nil {
			return fmt.Errorf("parse rev-list counts %q: %w", counts, err)
		}
		comparison.MergeBase, err = run(ctx, command{dir: mirror, args: []string{"merge-base", "--end-of-options", baseSHA, headSHA}})
		if err != nil {
			return fmt.Errorf("%s and %s have no common ancestor: %w", base, head, err)
		}
		return nil
	})
	return comparison, err
}

// withRange resolves base and head with one ls-remote, fetches both into the
// mirror and runs fn on their commits, all under the mirror lock.
func (c *Cache) withRange(ctx context.Context, repo *Repository, base, head string, fn func(mirror, baseSHA, headSHA string) error) (err error) {
	if c.Root == "" {
		return errors.New("git cache root is required")
	}
	refs, err := repo.LsRemote(ctx)
	if err != nil {
		return err
	}
	resolved := make([]Ref, 2)
	for i, name := range []string{base, head} {
		if resolved[i], err = refs.Resolve(name); err != nil {
			return fmt.Errorf("%s: %w", repo.URL.Redacted(), err)
		}
	}
	dir := filepath.Join(c.Root, repo.URL.CacheKey())
	mirror := filepath.Join(dir, "mirror.git")
	lock, err := acquire(ctx, filepath.Join(dir, "mirror.lock"), "mirror lock")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if err := ensureMirror(ctx, mirror); err != nil {
		return err
	}
	for i := range resolved {
		if resolved[i].SHA, err = fetchHistory(ctx, mirror, repo, resolved[i]); err != nil {
			return err
		}
	}
	return fn(mirror, resolved[0].SHA, resolved[1].SHA)
}

// fetchHistory makes ref's commit and its full history present in the
// mirror: a cached commit is reused unless an earlier Depth checkout left the
// mirror shallow, which would truncate a log or merge base.
func fetchHistory(ctx context.Context, mirror string, repo *Repository, ref Ref) (string, error) {
	shallow, err := run(ctx, command{dir: mirror, args: []string{"rev-parse", "--is-shallow-repository"}})
	if err != nil {
		return "", err
	}
	if shallow == "true" {
		return fetch(ctx, mirror, repo, ref, 0, "--unshallow")
	}
	if hasCommit(ctx, mirror, ref.SHA) {
		return ref.SHA, nil
	}
	return fetch(ctx, mirror, repo, ref, 0)
}

func parseLog(out string) ([]Commit, error) {
	var commits []Commit
	for _, record := range strings.Split(out, logRecord)[1:] {
		fields := strings.SplitN(record, "\x00", 9)
		if len(fields) != 9 {
			return nil, fmt.Errorf("parse git log record %q: expected 9 fields, got %d", record, len(fields))
		}
		date, err := time.Parse(time.RFC3339, fields[5])
		if err != nil {
			return nil, fmt.Errorf("parse commit %s date %q: %w", fields[0], fields[5], err)
		}
		commits = append(commits, Commit{SHA: fields[0], ShortSHA: fields[1], Parents: strings.Fields(fields[2]),
			Author: Signature{Name: fields[3], Email: fields[4]}, Date: date, Subject: fields[6],
			Body: strings.TrimSpace(fields[7]), Files: lines(fields[8])})
	}
	return commits, nil
}

// lines splits newline-separated output, dropping empty lines.
func lines(out string) []string {
	var result []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}
