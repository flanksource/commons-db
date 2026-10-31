package git

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/flanksource/commons-db/models"
)

type PullRequestCheck struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type PullRequestMetadata struct {
	Author          string             `json:"author"`
	AuthorAvatarURL string             `json:"authorAvatarUrl,omitempty"`
	CreatedAt       time.Time          `json:"createdAt"`
	BaseSHA         string             `json:"baseSha,omitempty"`
	HeadSHA         string             `json:"headSha"`
	HeadBranch      string             `json:"headBranch"`
	BaseBranch      string             `json:"baseBranch"`
	HeadBranchURL   string             `json:"headBranchUrl,omitempty"`
	HeadCommitURL   string             `json:"headCommitUrl,omitempty"`
	BaseBranchURL   string             `json:"baseBranchUrl,omitempty"`
	BaseCommitURL   string             `json:"baseCommitUrl,omitempty"`
	Mergeability    string             `json:"mergeability"`
	MergeReason     string             `json:"mergeReason,omitempty"`
	Draft           bool               `json:"draft,omitempty"`
	Checks          []PullRequestCheck `json:"checks"`
}

type PullRequestMetadataOptions struct {
	Number  int
	BaseSHA string
	// Listed is the pull request a ListPullRequests returned, read instead of
	// a GetPullRequest. GitHub's listing has no mergeability, so GitHub still
	// reads the pull request.
	Listed *PullRequest
	// SkipChecks reads no checks and leaves Checks nil.
	SkipChecks bool
}

// PullRequestReader is the part of a Provider pull request metadata reads.
type PullRequestReader interface {
	Type() string
	GetPullRequest(ctx context.Context, repo RepoURL, number int) (PullRequest, error)
}

type PullRequestChecksReader interface {
	PullRequestChecks(context.Context, RepoURL, PullRequest) ([]PullRequestCheck, error)
}

type WebRefOptions struct {
	Repository string
	Ref        string
	Kind       RefKind
}

func (u RepoURL) RefWebURL(options WebRefOptions) (string, error) {
	if options.Ref == "" || (options.Kind != RefBranch && options.Kind != RefSHA) {
		return "", fmt.Errorf("git web link needs a branch or commit, got %q %q", options.Kind, options.Ref)
	}
	if options.Repository != "" {
		owner, name, ok := strings.Cut(options.Repository, "/")
		if !ok || owner == "" || name == "" {
			return "", fmt.Errorf("invalid repository %q", options.Repository)
		}
		if u.Provider == models.ConnectionTypeAzureDevops {
			u.Project, u.Name = owner, name
		} else {
			parts := strings.Split(options.Repository, "/")
			u.Owner, u.Name = strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1]
		}
	}
	ref := url.PathEscape(options.Ref)
	base := "https://" + u.Host + "/" + u.Owner + "/" + u.Name
	switch u.Provider {
	case models.ConnectionTypeGithub:
		if options.Kind == RefBranch {
			return base + "/tree/" + ref, nil
		}
		return base + "/commit/" + ref, nil
	case models.ConnectionTypeGitlab:
		if options.Kind == RefBranch {
			return base + "/-/tree/" + ref, nil
		}
		return base + "/-/commit/" + ref, nil
	case models.ConnectionTypeAzureDevops:
		if options.Kind == RefBranch {
			return u.azureWebBase() + "?" + url.Values{"version": {"GB" + options.Ref}}.Encode(), nil
		}
		return u.azureWebBase() + "/commit/" + ref, nil
	default:
		return "", unsupported(u.Provider, "Git web links")
	}
}

func ReadPullRequestMetadata(ctx context.Context, provider PullRequestReader, repo RepoURL, options PullRequestMetadataOptions) (*PullRequestMetadata, error) {
	pr, err := metadataPullRequest(ctx, provider, repo, options)
	if err != nil {
		return nil, err
	}
	metadata := PullRequestMetadata{Mergeability: "unknown"}
	if pr.Metadata != nil {
		metadata = *pr.Metadata
	}
	metadata.Author, metadata.CreatedAt = pr.Author, pr.CreatedAt
	metadata.HeadSHA, metadata.HeadBranch, metadata.BaseBranch = pr.HeadSHA, pr.Head, pr.Base
	if options.BaseSHA != "" {
		metadata.BaseSHA = options.BaseSHA
	}
	if pr.State != PullRequestOpen {
		metadata.Mergeability = "not-applicable"
	}
	if err := setPullRequestLinks(repo, pr, &metadata); err != nil {
		return nil, err
	}
	if options.SkipChecks {
		return &metadata, nil
	}
	reader, ok := provider.(PullRequestChecksReader)
	if !ok {
		return &metadata, unsupported(provider.Type(), "pull request checks")
	}
	metadata.Checks, err = reader.PullRequestChecks(ctx, repo, pr)
	return &metadata, err
}

func metadataPullRequest(ctx context.Context, provider PullRequestReader, repo RepoURL, options PullRequestMetadataOptions) (PullRequest, error) {
	if options.Number <= 0 {
		return PullRequest{}, fmt.Errorf("pull request number must be positive")
	}
	if options.Listed != nil && options.Listed.Number != options.Number {
		return PullRequest{}, fmt.Errorf("listed pull request #%d is not #%d", options.Listed.Number, options.Number)
	}
	if options.Listed == nil || provider.Type() == models.ConnectionTypeGithub {
		return provider.GetPullRequest(ctx, repo, options.Number)
	}
	return *options.Listed, nil
}

func setPullRequestLinks(repo RepoURL, pr PullRequest, metadata *PullRequestMetadata) error {
	for _, link := range []struct {
		ref, repository string
		kind            RefKind
		result          *string
		source          bool
	}{
		{pr.Head, pr.HeadRepository, RefBranch, &metadata.HeadBranchURL, true},
		{pr.HeadSHA, pr.HeadRepository, RefSHA, &metadata.HeadCommitURL, true},
		{pr.Base, "", RefBranch, &metadata.BaseBranchURL, false},
		{metadata.BaseSHA, "", RefSHA, &metadata.BaseCommitURL, false},
	} {
		if link.ref == "" || (link.source && link.repository == "") {
			continue
		}
		value, err := repo.RefWebURL(WebRefOptions{Repository: link.repository, Ref: link.ref, Kind: link.kind})
		if err != nil {
			return err
		}
		*link.result = value
	}
	return nil
}
