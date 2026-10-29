package git

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// RemoteMergeBaseOptions selects two immutable commits in the target provider.
type RemoteMergeBaseOptions struct {
	Repository RepoURL
	BaseSHA    string
	HeadSHA    string
}

// RemoteHistoryProvider discovers shared history without downloading Git objects.
type RemoteHistoryProvider interface {
	RemoteMergeBase(context.Context, RemoteMergeBaseOptions) (string, error)
}

func (o RemoteMergeBaseOptions) validate() error {
	if !fullSHAPattern.MatchString(o.BaseSHA) || !fullSHAPattern.MatchString(o.HeadSHA) || o.Repository.Name == "" {
		return fmt.Errorf("remote ancestry requires a repository and full base and head commit SHAs")
	}
	return nil
}

func (p *azureProvider) RemoteMergeBase(ctx context.Context, o RemoteMergeBaseOptions) (string, error) {
	if err := o.validate(); err != nil {
		return "", err
	}
	var result struct {
		SHA string `json:"commonCommit"`
	}
	query := url.Values{"baseVersion": {o.BaseSHA}, "targetVersion": {o.HeadSHA}, "baseVersionType": {"commit"}, "targetVersionType": {"commit"}, "diffCommonCommit": {"true"}, "$top": {"0"}}
	endpoint := p.repoEndpoint(o.Repository, "/diffs/commits") + "&" + query.Encode()
	_, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: endpoint, Out: &result})
	return remoteBaseResult(ctx, p.api, result.SHA, err, p.repoEndpoint(o.Repository, "/commits/"+o.HeadSHA))
}

func (p *githubProvider) RemoteMergeBase(ctx context.Context, o RemoteMergeBaseOptions) (string, error) {
	if err := o.validate(); err != nil {
		return "", err
	}
	var result struct {
		Base struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	endpoint := p.repoEndpoint(o.Repository, "/compare/"+o.BaseSHA+"..."+o.HeadSHA) + "?per_page=1&page=2"
	_, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: endpoint, Out: &result})
	return remoteBaseResult(ctx, p.api, result.Base.SHA, err, p.repoEndpoint(o.Repository, "/commits/"+o.HeadSHA))
}

func (p *gitlabProvider) RemoteMergeBase(ctx context.Context, o RemoteMergeBaseOptions) (string, error) {
	if err := o.validate(); err != nil {
		return "", err
	}
	var result struct {
		SHA string `json:"id"`
	}
	endpoint := p.projectEndpoint(o.Repository, "/repository/merge_base") + "?" + url.Values{"refs[]": {o.BaseSHA, o.HeadSHA}}.Encode()
	_, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: endpoint, Out: &result})
	return remoteBaseResult(ctx, p.api, result.SHA, err, p.projectEndpoint(o.Repository, "/repository/commits/"+o.HeadSHA))
}

func remoteBaseResult(ctx context.Context, api *restClient, sha string, err error, headEndpoint string) (string, error) {
	if isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusConflict) {
		if _, headErr := api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: headEndpoint}); headErr != nil {
			return "", headErr
		}
		if strings.Contains(strings.ToLower(err.Error()), "no common ancestor") {
			return "", ErrUnrelatedHistories
		}
		if isStatus(err, http.StatusNotFound) {
			return "", fmt.Errorf("%w: candidate is unknown to remote", ErrRevisionNotFound)
		}
	}
	if err != nil {
		return "", err
	}
	if !fullSHAPattern.MatchString(sha) {
		return "", fmt.Errorf("%s returned invalid merge-base SHA %q", api.provider, sha)
	}
	return sha, nil
}
