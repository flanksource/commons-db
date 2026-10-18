package git

import (
	"fmt"
	"strings"
)

func isAzureHost(hostname string) bool {
	return hostname == "dev.azure.com" || hostname == "ssh.dev.azure.com" || strings.HasSuffix(hostname, ".visualstudio.com")
}

// parseAzurePath reads organization, project and repository from the Azure
// DevOps URL shapes:
//
//	https://[user@]dev.azure.com/{org}/{project}/_git/{repo}
//	https://{org}.visualstudio.com[/DefaultCollection]/{project}/_git/{repo}
//	git@ssh.dev.azure.com:v3/{org}/{project}/{repo}
//	git@vs-ssh.visualstudio.com:v3/{org}/{project}/{repo}
//	https://{server}/{collection...}/{project}/_git/{repo}  (Azure DevOps Server)
func parseAzurePath(parsed *RepoURL) error {
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	hostname := strings.ToLower(hostOnly(parsed.Host))
	switch {
	case hostname == "ssh.dev.azure.com" || hostname == "vs-ssh.visualstudio.com":
		if len(segments) != 4 || segments[0] != "v3" {
			return fmt.Errorf("azure DevOps SSH path must be v3/org/project/repo, got %q", parsed.Path)
		}
		return setAzure(parsed, segments[1], segments[2], segments[3])
	case hostname == "dev.azure.com":
		if len(segments) != 4 || segments[2] != "_git" {
			return fmt.Errorf("azure DevOps path must be org/project/_git/repo, got %q", parsed.Path)
		}
		return setAzure(parsed, segments[0], segments[1], segments[3])
	case strings.HasSuffix(hostname, ".visualstudio.com"):
		if len(segments) > 0 && strings.EqualFold(segments[0], "DefaultCollection") {
			segments = segments[1:]
		}
		if len(segments) != 3 || segments[1] != "_git" {
			return fmt.Errorf("azure DevOps path must be [DefaultCollection/]project/_git/repo, got %q", parsed.Path)
		}
		return setAzure(parsed, strings.TrimSuffix(hostname, ".visualstudio.com"), segments[0], segments[2])
	}
	marker := len(segments) - 2
	if marker < 2 || segments[marker] != "_git" {
		return fmt.Errorf("azure DevOps Server path must be collection/project/_git/repo, got %q", parsed.Path)
	}
	return setAzure(parsed, strings.Join(segments[:marker-1], "/"), segments[marker-1], segments[marker+1])
}

func setAzure(parsed *RepoURL, org, project, name string) error {
	name = strings.TrimSuffix(name, ".git")
	if org == "" || project == "" || name == "" {
		return fmt.Errorf("azure DevOps path %q has an empty organization, project or repository", parsed.Path)
	}
	parsed.Owner, parsed.Project, parsed.Name = org, project, name
	return nil
}
