package git

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/flanksource/commons-db/models"
)

// ConnectionScope is what a git connection may open, read from the connection
// alone: no secret is hydrated and no client is built, so a catalog can be
// matched against a checkout's remote. Client.Repository refuses exactly the
// repositories Owns refuses.
type ConnectionScope struct {
	Type string
	// Repository is the one repository a git connection names; nil for a
	// provider connection.
	Repository *RepoURL
	// API is the base URL a provider connection calls; nil for a git
	// connection.
	API *url.URL
	// Namespace is the GitHub organization, GitLab group or Azure DevOps
	// project the connection is limited to; empty for none.
	Namespace string
}

// ScopeOf reads the scope of conn. Type git takes its URL as the repository;
// github, gitlab and azure_devops take it as the API (public GitHub when
// empty) and the namespace from Properties organization, group or project.
func ScopeOf(conn models.Connection) (ConnectionScope, error) {
	switch conn.Type {
	case models.ConnectionTypeGit:
		repo, err := ParseRepoURL(conn.URL, models.ConnectionTypeGit)
		if err != nil {
			return ConnectionScope{}, fmt.Errorf("git connection %q: %w", conn.Name, err)
		}
		return ConnectionScope{Type: conn.Type, Repository: &repo}, nil
	case models.ConnectionTypeGithub, models.ConnectionTypeGitlab, models.ConnectionTypeAzureDevops:
		raw := conn.URL
		if raw == "" && conn.Type == models.ConnectionTypeGithub {
			raw = githubAPI
		}
		if raw == "" {
			return ConnectionScope{}, fmt.Errorf("%s API URL is required", conn.Type)
		}
		api, err := parseAPIBase(conn.Type, raw)
		if err != nil {
			return ConnectionScope{}, err
		}
		return ConnectionScope{Type: conn.Type, API: api, Namespace: conn.Properties[namespaceProperty(conn.Type)]}, nil
	}
	return ConnectionScope{}, fmt.Errorf("connection %q has type %q, which is not a git provider", conn.Name, conn.Type)
}

func namespaceProperty(connectionType string) string {
	switch connectionType {
	case models.ConnectionTypeGithub:
		return "organization"
	case models.ConnectionTypeGitlab:
		return "group"
	}
	return "project"
}

// parseAPIBase is a provider's API base URL: HTTPS, with no query or fragment.
func parseAPIBase(provider, raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("%s API URL must be an HTTPS URL", provider)
	}
	base.RawQuery, base.Fragment = "", ""
	return base, nil
}

// Owns refuses a repository outside the connection's host and namespace, or
// one reached over http when the connection is configured over https (its
// credentials would travel unencrypted). A git connection owns any
// repository on its host; SameRepository tells whether it is the one it names.
func (s ConnectionScope) Owns(repo RepoURL) error {
	if scheme := s.scheme(); strings.EqualFold(repo.Scheme, "http") && !strings.EqualFold(scheme, "http") {
		return fmt.Errorf("scheme %q would send the connection's %s credentials unencrypted: use https", repo.Scheme, scheme)
	}
	switch s.Type {
	case models.ConnectionTypeGit:
		if !strings.EqualFold(hostOnly(repo.Host), hostOnly(s.Repository.Host)) {
			return fmt.Errorf("host %q is not the connection's host %q", repo.Host, s.Repository.Host)
		}
	case models.ConnectionTypeGithub, models.ConnectionTypeGitlab:
		if host := s.webHost(); !strings.EqualFold(hostOnly(repo.Host), host) {
			return fmt.Errorf("host %q is not the connection's host %q", repo.Host, host)
		}
		if s.Namespace != "" && !strings.EqualFold(repo.Owner, s.Namespace) &&
			!strings.HasPrefix(strings.ToLower(repo.Owner), strings.ToLower(s.Namespace)+"/") {
			return fmt.Errorf("owner %q is outside the connection's namespace %q", repo.Owner, s.Namespace)
		}
	case models.ConnectionTypeAzureDevops:
		return s.ownsAzure(repo)
	}
	return nil
}

// RepositoryName is the name a provider connection lists repo by
// (RepositoryInfo.FullName, what browse.FindRepository resolves): owner/name
// on GitHub and GitLab, project/name on Azure DevOps, whose organization the
// connection fixes. A git connection pins one repository and names none.
func (s ConnectionScope) RepositoryName(repo RepoURL) string {
	switch s.Type {
	case models.ConnectionTypeGit:
		return ""
	case models.ConnectionTypeAzureDevops:
		return repo.Project + "/" + repo.Name
	}
	return repo.FullName()
}

// scheme is the scheme of the URL the connection was configured with: the
// repository of a git connection, the API of a provider.
func (s ConnectionScope) scheme() string {
	if s.Repository != nil {
		return s.Repository.Scheme
	}
	return s.API.Scheme
}

// webHost is the host repositories live on: api.github.com serves github.com,
// any other API (GitHub Enterprise, GitLab) shares its host.
func (s ConnectionScope) webHost() string {
	host := strings.ToLower(s.API.Hostname())
	if host == "api.github.com" {
		return "github.com"
	}
	return host
}

// ownsAzure accepts a repository of the connection's organization (and
// project, when set) on Azure DevOps Services when the connection is to
// Services, or on the connection's own host for Azure DevOps Server.
func (s ConnectionScope) ownsAzure(repo RepoURL) error {
	repoHost, connHost := strings.ToLower(hostOnly(repo.Host)), strings.ToLower(s.API.Hostname())
	if isAzureHost(connHost) != isAzureHost(repoHost) || (!isAzureHost(connHost) && repoHost != connHost) {
		return fmt.Errorf("host %q is not the connection's Azure DevOps host %q", repo.Host, s.API.Host)
	}
	org := azureOrganization(s.API)
	if !strings.EqualFold(repo.Owner, org) {
		return fmt.Errorf("organization %q is not the connection's organization %q", repo.Owner, org)
	}
	if s.Namespace != "" && !strings.EqualFold(repo.Project, s.Namespace) {
		return fmt.Errorf("project %q is not the connection's project %q", repo.Project, s.Namespace)
	}
	return nil
}

// azureOrganization reads the organization from https://dev.azure.com/{org},
// https://{org}.visualstudio.com or a server's collection path.
func azureOrganization(base *url.URL) string {
	host := strings.ToLower(base.Hostname())
	if strings.HasSuffix(host, ".visualstudio.com") {
		return strings.TrimSuffix(host, ".visualstudio.com")
	}
	return strings.Trim(base.Path, "/")
}

// SameRepository reports whether u and other address one repository, whatever
// the transport: host without port and path without a .git suffix, compared
// case-insensitively (a local path exactly).
func (u RepoURL) SameRepository(other RepoURL) bool {
	if (u.Scheme == "file") != (other.Scheme == "file") {
		return false
	}
	if u.Scheme == "file" {
		return repositoryPath(u.Path) == repositoryPath(other.Path)
	}
	return strings.EqualFold(hostOnly(u.Host), hostOnly(other.Host)) &&
		strings.EqualFold(repositoryPath(u.Path), repositoryPath(other.Path))
}

func repositoryPath(path string) string {
	return strings.Trim(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
}
