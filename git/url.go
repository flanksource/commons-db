package git

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/models"
)

// RepoURL is a parsed repository URL. Clone is what git is given: the URL
// without query, fragment or password.
type RepoURL struct {
	Clone    string
	Scheme   string
	Host     string
	Path     string
	Provider string
	// Owner is the GitHub owner, the GitLab group path (nested groups joined
	// with "/"), or the Azure DevOps organization.
	Owner string
	// Project is the Azure DevOps project; empty elsewhere.
	Project string
	Name    string
	// Ref and Depth come from the ?ref= and ?depth= query parameters.
	Ref   string
	Depth *int
	User  string
}

var (
	scpPattern     = regexp.MustCompile(`^(?:([^@/:\s]+)@)?([^@/:\s]+):(.*)$`)
	userInfoInText = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*://)[^\s/@'"]+@`)
	cacheKeyUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

var ErrNoCommitView = errors.New("no web commit view")

// ParseRepoURL parses an HTTP(S), SSH, scp-style, git://, file:// URL or a
// local path. providerHint (a models.ConnectionType* git provider) overrides
// host-based provider detection, e.g. for a self-hosted GitLab.
func ParseRepoURL(raw, providerHint string) (RepoURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RepoURL{}, errors.New("repository URL is required")
	}
	parsed, err := splitRepoURL(raw)
	if err != nil {
		return RepoURL{}, err
	}
	if parsed.Provider, err = detectProvider(parsed.Host, providerHint); err != nil {
		return RepoURL{}, err
	}
	if err := parseRepoPath(&parsed); err != nil {
		return RepoURL{}, fmt.Errorf("repository URL %s: %w", Redact(raw), err)
	}
	return parsed, nil
}

// SourceRepository resolves a pull request's provider-native source name into
// a repository on the same host, including fork and Azure project identity.
func (u RepoURL) SourceRepository(name string) (RepoURL, error) {
	parts := strings.Split(name, "/")
	if len(parts) < 2 || strings.ContainsAny(name, "?#:@\\") {
		return RepoURL{}, fmt.Errorf("invalid PR source repository %q", name)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return RepoURL{}, fmt.Errorf("invalid PR source repository %q", name)
		}
	}
	path := "/" + name
	if u.Provider == models.ConnectionTypeAzureDevops {
		if len(parts) != 2 {
			return RepoURL{}, fmt.Errorf("azure PR source must be project/repository, got %q", name)
		}
		u.Project, u.Name = parts[0], parts[1]
		return ParseRepoURL(u.azureWebBase(), u.Provider)
	}
	return ParseRepoURL((&url.URL{Scheme: "https", Host: u.Host, Path: path}).String(), u.Provider)
}

func splitRepoURL(raw string) (RepoURL, error) {
	if strings.Contains(raw, "://") {
		return parseSchemeURL(raw)
	}
	if match := scpPattern.FindStringSubmatch(raw); match != nil {
		location, query, _ := strings.Cut(match[3], "?")
		parsed := RepoURL{Scheme: "ssh", Host: match[2], User: match[1], Path: strings.Trim(location, "/")}
		parsed.Clone = strings.TrimSuffix(raw, "?"+query)
		return parsed, parseRepoQuery(&parsed, query)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return RepoURL{}, fmt.Errorf("resolve repository path %q: %w", raw, err)
	}
	return RepoURL{Clone: abs, Scheme: "file", Path: abs}, nil
}

func parseSchemeURL(raw string) (RepoURL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return RepoURL{}, fmt.Errorf("parse repository URL %s: %w", Redact(raw), err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https", "http", "ssh", "git", "file":
	default:
		return RepoURL{}, fmt.Errorf("unsupported repository URL scheme %q", u.Scheme)
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return RepoURL{}, fmt.Errorf("repository URL %s must not embed a password: set it on the connection", Redact(raw))
	}
	parsed := RepoURL{Scheme: scheme, Host: u.Host, User: u.User.Username(), Path: strings.TrimPrefix(u.Path, "/")}
	if scheme == "file" {
		parsed.Path = u.Path
	}
	if err := parseRepoQuery(&parsed, u.RawQuery); err != nil {
		return RepoURL{}, err
	}
	u.Scheme, u.RawQuery, u.Fragment = scheme, "", ""
	parsed.Clone = u.String()
	return parsed, nil
}

func parseRepoQuery(parsed *RepoURL, rawQuery string) error {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return fmt.Errorf("parse repository URL query: %w", err)
	}
	for key := range query {
		if key != "ref" && key != "depth" {
			return fmt.Errorf("unsupported query parameter %q in repository URL (only ref and depth)", key)
		}
	}
	parsed.Ref = query.Get("ref")
	if raw := query.Get("depth"); raw != "" {
		depth, err := strconv.Atoi(raw)
		if err != nil || depth < 0 {
			return fmt.Errorf("invalid depth %q in repository URL: must be a non-negative integer", raw)
		}
		parsed.Depth = &depth
	}
	return nil
}

func detectProvider(host, hint string) (string, error) {
	switch hint {
	case models.ConnectionTypeGit, models.ConnectionTypeGithub, models.ConnectionTypeGitlab, models.ConnectionTypeAzureDevops:
		return hint, nil
	case "":
	default:
		return "", fmt.Errorf("unsupported git provider %q", hint)
	}
	switch hostname := strings.ToLower(hostOnly(host)); {
	case hostname == "github.com":
		return models.ConnectionTypeGithub, nil
	case hostname == "gitlab.com":
		return models.ConnectionTypeGitlab, nil
	case isAzureHost(hostname):
		return models.ConnectionTypeAzureDevops, nil
	}
	return models.ConnectionTypeGit, nil
}

func parseRepoPath(parsed *RepoURL) error {
	segments := strings.Split(strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/"), "/")
	switch parsed.Provider {
	case models.ConnectionTypeGithub:
		if len(segments) != 2 || segments[0] == "" || segments[1] == "" {
			return fmt.Errorf("GitHub repository path must be owner/name, got %q", parsed.Path)
		}
	case models.ConnectionTypeGitlab:
		if len(segments) < 2 || slices.Contains(segments, "") {
			return fmt.Errorf("GitLab repository path must be group[/subgroup...]/name, got %q", parsed.Path)
		}
	case models.ConnectionTypeAzureDevops:
		return parseAzurePath(parsed)
	}
	parsed.Name = segments[len(segments)-1]
	parsed.Owner = strings.Join(segments[:len(segments)-1], "/")
	return nil
}

func hostOnly(host string) string {
	if h, _, ok := strings.Cut(host, ":"); ok {
		return h
	}
	return host
}

// FullName is the provider's repository path without the host.
func (u RepoURL) FullName() string {
	parts := slices.DeleteFunc([]string{u.Owner, u.Project, u.Name}, func(part string) bool { return part == "" })
	return strings.Join(parts, "/")
}

// Redacted is the clone URL with any user info removed, for display.
func (u RepoURL) Redacted() string {
	if u.Scheme == "ssh" && !strings.Contains(u.Clone, "://") {
		return u.Host + ":" + u.Path
	}
	return Redact(u.Clone)
}

// Short is host/path without scheme, user or query; a local path for file
// URLs.
func (u RepoURL) Short() string {
	if u.Scheme == "file" {
		return u.Path
	}
	return u.Host + "/" + u.Path
}

// CacheKey is a filesystem-safe, collision-resistant directory name.
func (u RepoURL) CacheKey() string {
	sum := sha256.Sum256([]byte(u.Clone))
	readable := strings.Trim(cacheKeyUnsafe.ReplaceAllString(strings.TrimSuffix(u.Short(), ".git"), "_"), "_")
	if len(readable) > 80 {
		readable = readable[len(readable)-80:]
	}
	return readable + "-" + hex.EncodeToString(sum[:])[:12]
}

func (u RepoURL) CommitURL(sha string) (string, error) {
	if !fullSHAPattern.MatchString(sha) {
		return "", fmt.Errorf("commit URL: give the full 40-character commit SHA")
	}
	switch u.Provider {
	case models.ConnectionTypeGithub:
		return u.webBase() + "/commit/" + sha, nil
	case models.ConnectionTypeGitlab:
		return u.webBase() + "/-/commit/" + sha, nil
	case models.ConnectionTypeAzureDevops:
		return u.azureWebBase() + "/commit/" + sha, nil
	}
	return "", ErrNoCommitView
}

// Redact removes user info from every URL in text, e.g. git error output.
func Redact(text string) string {
	return userInfoInText.ReplaceAllString(text, "$1")
}
