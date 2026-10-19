package git

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/flanksource/commons-db/models"
)

// ErrNoFileView is returned by FileURL for a repository whose host has no
// known web file view: a generic git host or a local repository.
var ErrNoFileView = errors.New("no web file view")

// FileURL is the provider's web page for path (repository-relative,
// slash-separated) on ref: a branch or tag by its name, so the page follows
// the branch; any other ref (a SHA, a detached HEAD) by its commit.
func (u RepoURL) FileURL(ref Ref, path string) (string, error) {
	version, err := fileVersion(ref)
	if err != nil {
		return "", err
	}
	path = strings.Trim(path, "/")
	if path == "" {
		return "", errors.New("file URL: path is required")
	}
	switch u.Provider {
	case models.ConnectionTypeGithub:
		return u.webBase() + "/blob/" + escapePath(version.name) + "/" + escapePath(path), nil
	case models.ConnectionTypeGitlab:
		return u.webBase() + "/-/blob/" + escapePath(version.name) + "/" + escapePath(path), nil
	case models.ConnectionTypeAzureDevops:
		query := url.Values{"path": {"/" + path}, "version": {version.azurePrefix + version.name}}
		return u.azureWebBase() + "?" + query.Encode(), nil
	}
	return "", fmt.Errorf("file URL: %s repository %s: %w", u.Provider, u.Redacted(), ErrNoFileView)
}

// fileRefVersion is what a file link names: a branch, tag or commit, with
// the prefix Azure DevOps marks its kind by (GB, GT, GC).
type fileRefVersion struct {
	name        string
	azurePrefix string
}

func fileVersion(ref Ref) (fileRefVersion, error) {
	switch ref.Kind {
	case RefBranch, RefTag:
		if strings.TrimSpace(ref.Name) == "" {
			return fileRefVersion{}, fmt.Errorf("file URL: %s name is required", ref.Kind)
		}
		prefix := map[RefKind]string{RefBranch: "GB", RefTag: "GT"}[ref.Kind]
		return fileRefVersion{name: ref.Name, azurePrefix: prefix}, nil
	}
	if strings.TrimSpace(ref.SHA) == "" {
		return fileRefVersion{}, errors.New("file URL: commit is required")
	}
	return fileRefVersion{name: ref.SHA, azurePrefix: "GC"}, nil
}

func (u RepoURL) webScheme() string {
	if u.Scheme == "http" {
		return "http"
	}
	return "https"
}

// webBase is scheme://host/owner/name; an SSH clone's port is the SSH port,
// not the web one, so it is dropped.
func (u RepoURL) webBase() string {
	host := u.Host
	if u.Scheme == "ssh" {
		host = hostOnly(host)
	}
	return u.webScheme() + "://" + host + "/" + escapePath(u.Owner+"/"+u.Name)
}

func (u RepoURL) azureWebBase() string {
	repo := "/" + url.PathEscape(u.Project) + "/_git/" + url.PathEscape(u.Name)
	switch hostname := strings.ToLower(hostOnly(u.Host)); {
	case hostname == "dev.azure.com" || hostname == "ssh.dev.azure.com":
		return "https://dev.azure.com/" + url.PathEscape(u.Owner) + repo
	case hostname == "vs-ssh.visualstudio.com" || strings.HasSuffix(hostname, ".visualstudio.com"):
		return "https://" + url.PathEscape(u.Owner) + ".visualstudio.com" + repo
	}
	return u.webScheme() + "://" + u.Host + "/" + escapePath(u.Owner) + repo
}

func escapePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}
