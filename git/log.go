package git

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Commit is one commit as Cache.Log or Cache.History reads it. Files are the
// paths it changed against its first parent (a rename lists its new path); a
// merge commit (len(Parents) > 1) lists none. Status and Renames are filled
// by History only: how each of Files changed, and which renamed from what.
type Commit struct {
	SHA      string
	ShortSHA string
	Subject  string
	Body     string
	Author   Signature
	Date     time.Time
	Parents  []string
	Files    []string
	Status   map[string]FileStatus
	Renames  []Rename
}

// FileStatus is git's --name-status letter for one changed path.
type FileStatus string

const (
	FileAdded       FileStatus = "A"
	FileModified    FileStatus = "M"
	FileDeleted     FileStatus = "D"
	FileRenamed     FileStatus = "R"
	FileCopied      FileStatus = "C"
	FileTypeChanged FileStatus = "T"
)

// Rename is a path a commit moved From one name To another.
type Rename struct {
	From string
	To   string
}

// IsMerge reports whether the commit has more than one parent.
func (c Commit) IsMerge() bool { return len(c.Parents) > 1 }

// LogOptions configures Cache.Log. Limit > 0 keeps only the newest Limit
// commits; Paths (matched literally, never as globs) keeps only the commits
// that touch them. HeadFrom, when set, is the repository head is read from
// (e.g. a fork of the logged repository): it resolves and is fetched there,
// into the logged repository's mirror. A head sharing no history with base
// lists every commit whose patch base lacks; MergeBase tells the two apart.
type LogOptions struct {
	Base     string
	Head     string
	Limit    int
	Paths    []string
	HeadFrom *Repository
}

// HistoryOptions configures Cache.History. Paths (matched literally, never as
// globs) keeps only the commits that touch them; Follow continues one path's
// history past its renames (git log --follow, so exactly one path); Limit > 0
// keeps the newest Limit commits; Since is a git date (--since) bounding how
// far back to read. Detail controls file metadata; its default detects renames.
type HistoryOptions struct {
	Detail HistoryDetail
	Paths  []string
	Follow bool
	Limit  int
	Since  string
	Offset int
}

// HistoryDetail controls the file metadata included with each commit.
type HistoryDetail string

const (
	HistoryStatus   HistoryDetail = ""
	HistoryMetadata HistoryDetail = "metadata"
	HistoryNames    HistoryDetail = "names"
)

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

// Log returns the commits reachable from head and not from base, oldest
// first: parents before children (--topo-order), so a merge commit follows
// every commit it merges. A commit whose patch base already carries (a
// cherry-pick of it, by patch-id) is omitted: git log --cherry-pick
// --right-only base...head. Merge commits are kept. options.Base and
// options.Head resolve like Checkout refs; both are fetched into the mirror first.
func (c *Cache) Log(ctx context.Context, repo *Repository, options LogOptions) ([]Commit, error) {
	if options.Limit < 0 {
		return nil, fmt.Errorf("invalid log limit %d", options.Limit)
	}
	args := []string{"-c", "core.quotePath=false", "--literal-pathspecs", "log", "--cherry-pick", "--right-only", "--topo-order", "--reverse",
		"--no-renames", "--name-only", logFormat}
	if options.Limit > 0 {
		args = append(args, "--max-count="+strconv.Itoa(options.Limit))
	}
	headFrom := cmp.Or(options.HeadFrom, repo)
	var out string
	err := c.withRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, options.Base}, {headFrom, options.Head}}, History: true}, func(mirror string, shas []string) (err error) {
		args = append(args, "--end-of-options", shas[0]+"..."+shas[1], "--")
		out, err = run(ctx, command{dir: mirror, args: append(args, options.Paths...)})
		return err
	})

	if err != nil {
		return nil, err
	}
	return parseLog(out, parseNameOnly)
}

// History returns the commits reachable from ref, newest first
// (--topo-order), with each commit's per-file changes (--name-status, renames
// detected). ref resolves like a Checkout ref and its full history is fetched
// into the mirror, unshallowing it when needed.
func (c *Cache) History(ctx context.Context, repo *Repository, ref string, options HistoryOptions) ([]Commit, error) {
	if options.Limit < 0 || options.Offset < 0 {
		return nil, fmt.Errorf("invalid history limit %d", options.Limit)
	}
	if options.Follow && len(options.Paths) != 1 {
		return nil, fmt.Errorf("history with Follow needs exactly one path, got %d", len(options.Paths))
	}
	args := []string{"-c", "core.quotePath=false", "--literal-pathspecs", "log", "--topo-order", logFormat}
	parseFiles := parseNameStatus
	switch options.Detail {
	case HistoryStatus:
		args = append(args, "--find-renames", "--name-status")
	case HistoryNames:
		args = append(args, "--no-renames", "--name-only")
		parseFiles = parseNameOnly
	case HistoryMetadata:
		args = append(args, "--no-patch")
		parseFiles = parseNameOnly
	default:
		return nil, fmt.Errorf("invalid history detail %q", options.Detail)
	}
	if options.Follow {
		args = append(args, "--follow")
	}
	if options.Limit > 0 {
		args = append(args, "--max-count="+strconv.Itoa(options.Limit))
	}
	if options.Since != "" {
		args = append(args, "--since="+options.Since)
	}
	if options.Offset > 0 {
		args = append(args, "--skip="+strconv.Itoa(options.Offset))
	}
	var out string
	err := c.withRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, ref}}, History: true}, func(mirror string, shas []string) (err error) {
		out, err = run(ctx, command{dir: mirror, args: slices.Concat(args, []string{"--end-of-options", shas[0], "--"}, options.Paths)})
		return err
	})

	if err != nil {
		return nil, err
	}
	return parseLog(out, parseFiles)
}

// Compare counts the commits head is ahead of and behind base and finds
// their merge base; refs without a common ancestor are an error.
func (c *Cache) Compare(ctx context.Context, repo *Repository, base, head string) (Comparison, error) {
	var comparison Comparison
	err := c.withRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, base}, {repo, head}}, History: true}, func(mirror string, shas []string) error {
		baseSHA, headSHA := shas[0], shas[1]
		counts, err := run(ctx, command{dir: mirror, args: []string{"rev-list", "--left-right", "--count", "--end-of-options", baseSHA + "..." + headSHA}})
		if err != nil {
			return err
		}
		if _, err := fmt.Sscanf(counts, "%d\t%d", &comparison.Behind, &comparison.Ahead); err != nil {
			return fmt.Errorf("parse rev-list counts %q: %w", counts, err)
		}
		if comparison.MergeBase, err = mergeBase(ctx, mirror, baseSHA, headSHA); err != nil {
			return fmt.Errorf("%s and %s: %w", base, head, err)
		}
		return nil
	})

	return comparison, err
}

// MergeBaseOptions configures Cache.MergeBase. HeadFrom is the repository
// Head is read from (e.g. a fork); nil is the base's own.
type MergeBaseOptions struct {
	Base     string
	Head     string
	HeadFrom *Repository
}

// MergeBase is the best common ancestor of base and head, both fetched with
// their full history into repo's mirror; refs sharing no history fail with
// ErrUnrelatedHistories.
func (c *Cache) MergeBase(ctx context.Context, repo *Repository, options MergeBaseOptions) (string, error) {
	base, head := options.Base, options.Head
	headFrom := cmp.Or(options.HeadFrom, repo)
	var found string
	err := c.withRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, base}, {headFrom, head}}, History: true}, func(mirror string, shas []string) (err error) {
		if found, err = mergeBase(ctx, mirror, shas[0], shas[1]); err != nil {
			return fmt.Errorf("%s of %s and %s of %s: %w", base, repo.URL.Redacted(), head, headFrom.URL.Redacted(), err)
		}
		return nil
	})

	return found, err
}

// mergeBase is the best common ancestor of commits a and b in dir; commits
// without one fail with ErrUnrelatedHistories.
func mergeBase(ctx context.Context, dir, a, b string) (string, error) {
	base, err := run(ctx, command{dir: dir, args: []string{"merge-base", "--end-of-options", a, b}})
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 && base == "" {
		return "", ErrUnrelatedHistories
	}
	return base, err
}

// refOn is a ref name on the repository it resolves and is fetched from.
type refOn struct {
	repo *Repository
	name string
}

type refReadOptions struct {
	Refs    []refOn
	History bool
}

// withRefs prepares and retains immutable commits under the writer lock, then
// reads them outside the lock. Full object IDs do not need remote discovery.
func (c *Cache) withRefs(ctx context.Context, repo *Repository, options refReadOptions, fn func(mirror string, shas []string) error) error {
	directory, shas, err := c.prepareReadRefs(ctx, repo, options)
	if err != nil {
		return err
	}
	return readRepository(ctx, directory, func() error { return fn(directory, shas) })
}

func (c *Cache) prepareReadRefs(ctx context.Context, repo *Repository, options refReadOptions) (string, []string, error) {
	if c.Root == "" {
		return "", nil, errors.New("git cache root is required")
	}
	if repo == nil {
		return "", nil, errors.New("git repository is required")
	}
	resolved, err := resolveReadRefs(ctx, options.Refs)
	if err != nil {
		return "", nil, err
	}
	shas := make([]string, len(resolved))
	var directory string
	err = c.withMirror(ctx, repo, func(mirror string) (err error) {
		directory = mirror
		var updates strings.Builder
		retained := map[string]bool{}
		for i, ref := range resolved {
			shas[i] = ref.SHA
			fetching := fetchOptions{Dir: mirror, Owner: repo, Repo: options.Refs[i].repo, Ref: ref}
			if options.History {
				shas[i], err = fetchHistory(ctx, fetching)
			} else if !hasCommit(ctx, mirror, ref.SHA) {
				shas[i], err = fetch(ctx, fetching)
			}
			if err != nil {
				return err
			}
			if !retained[shas[i]] {
				fmt.Fprintf(&updates, "update refs/commons-cache/commits/%s %s\n", shas[i], shas[i])
				retained[shas[i]] = true
			}
		}
		_, err = run(ctx, command{dir: mirror, args: []string{"update-ref", "--stdin"}, stdin: []byte(updates.String())})
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return directory, shas, nil
}

func resolveReadRefs(ctx context.Context, refs []refOn) ([]Ref, error) {
	listed := map[*Repository]RemoteRefs{}
	resolved := make([]Ref, len(refs))
	var err error
	for i, ref := range refs {
		if ref.repo == nil {
			return nil, errors.New("git read reference requires a repository")
		}
		if fullSHAPattern.MatchString(ref.name) {
			resolved[i] = Ref{Name: ref.name, FullName: ref.name, SHA: ref.name, Kind: RefSHA}
			continue
		}
		remote, ok := listed[ref.repo]
		if !ok {
			if remote, err = ref.repo.LsRemote(ctx); err != nil {
				return nil, err
			}
			listed[ref.repo] = remote
		}
		if resolved[i], err = remote.Resolve(ref.name); err != nil {
			return nil, fmt.Errorf("%s: %w", ref.repo.URL.Redacted(), err)
		}
	}
	return resolved, nil
}

// withMirror runs fn on repo's bare mirror, created when missing, under the
// mirror lock.
func (c *Cache) withMirror(ctx context.Context, repo *Repository, fn func(mirror string) error) (err error) {
	if c.Root == "" {
		return errors.New("git cache root is required")
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
	return fn(mirror)
}

// fetchHistory makes ref's commit and its full history, fetched from repo,
// present in owner's mirror: a cached commit is reused unless an earlier
// Depth checkout left the mirror shallow, which would truncate a log or merge
// base.
func fetchHistory(ctx context.Context, options fetchOptions) (string, error) {
	shallow, err := run(ctx, command{dir: options.Dir, args: []string{"rev-parse", "--is-shallow-repository"}})
	if err != nil {
		return "", err
	}
	if shallow == "true" {
		options.Flags = append(slices.Clone(options.Flags), "--unshallow")
		return fetch(ctx, options)
	}
	if hasCommit(ctx, options.Dir, options.Ref.SHA) {
		return options.Ref.SHA, nil
	}
	return fetch(ctx, options)
}

// parseLog splits logFormat output into commits; parseFiles reads the file
// list that follows each commit's header (--name-only or --name-status).
func parseLog(out string, parseFiles func(commit *Commit, files string) error) ([]Commit, error) {
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
		commit := Commit{SHA: fields[0], ShortSHA: fields[1], Parents: strings.Fields(fields[2]),
			Author: Signature{Name: fields[3], Email: fields[4]}, Date: date, Subject: fields[6],
			Body: strings.TrimSpace(fields[7])}
		if err := parseFiles(&commit, fields[8]); err != nil {
			return nil, err
		}
		commits = append(commits, commit)
	}
	return commits, nil
}

func parseNameOnly(commit *Commit, files string) error {
	commit.Files = lines(files)
	return nil
}

// parseNameStatus reads --name-status lines: "<status>\t<path>", or
// "<R|C><score>\t<from>\t<to>" for a rename or copy.
func parseNameStatus(commit *Commit, files string) error {
	for _, line := range lines(files) {
		parts := strings.Split(line, "\t")
		status := FileStatus(strings.TrimRight(parts[0], "0123456789"))
		var path string
		switch {
		case len(parts) == 3 && (status == FileRenamed || status == FileCopied):
			path = parts[2]
			if status == FileRenamed {
				commit.Renames = append(commit.Renames, Rename{From: parts[1], To: path})
			}
		case len(parts) == 2 && slices.Contains([]FileStatus{FileAdded, FileModified, FileDeleted, FileTypeChanged}, status):
			path = parts[1]
		default:
			return fmt.Errorf("parse git log --name-status line %q of commit %s", line, commit.SHA)
		}
		if commit.Status == nil {
			commit.Status = map[string]FileStatus{}
		}
		commit.Files = append(commit.Files, path)
		commit.Status[path] = status
	}
	return nil
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
