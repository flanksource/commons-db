package git

import (
	"errors"
	"fmt"

	"github.com/flanksource/commons-db/models"
)

var ErrNoCommitView = errors.New("no web commit view")

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
