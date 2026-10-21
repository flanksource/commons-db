# Pull request listing metadata

Use `Provider.ListPullRequests` to obtain PRs into a target branch. Apply application pagination before calling `ReadPullRequestMetadata` for the visible rows. Metadata reads use the existing provider connection and never start checks or modify a pull request.

```go
pulls, err := provider.ListPullRequests(ctx, repository, git.ListPullRequestsOptions{
    Base: "release/qa",
    State: git.PullRequestOpen,
    Limit: 6,
})
if err != nil {
    return err
}
for _, pull := range pulls[:min(5, len(pulls))] {
    metadata, err := git.ReadPullRequestMetadata(ctx, provider, repository, git.PullRequestMetadataOptions{
        Number: pull.Number,
        BaseSHA: targetSHA,
    })
    if err != nil {
        return err
    }
    render(pull, metadata)
}
```

`ctx` bounds upstream reads. `provider` is the authenticated repository provider. `repository` is its parsed `RepoURL`. `Number` selects a positive PR number. `BaseSHA` supplies the target branch revision resolved by the caller; when omitted, metadata preserves the revision reported by the provider.

Metadata includes the author and avatar, source and target revisions, provider-built branch/commit URLs, draft state, mergeability, the provider's merge reason and named check outcomes. GitHub combines commit statuses and check runs. Azure includes PR statuses and policy evaluations. GitLab reads commit statuses and detailed merge status. Source links use the fork repository when present; a deleted source repository has no fabricated link.

Mergeability and check success are separate signals. `mergeable` means a merge has no reported conflicts; it does not certify that every approval or required check has passed. Unknown or pending provider state remains unknown or checking. Check retrieval failures return the readable metadata together with an error, allowing callers to retain revision links while explicitly reporting unavailable check data.

`RepoURL.RefWebURL` builds an upstream link for `WebRefOptions`. `Ref` names the branch or commit, `Kind` is `RefBranch` or `RefSHA`, and optional `Repository` selects a different repository path for a fork.

Run the provider fixture tests with:

```bash
go test ./git -ginkgo.focus='pull request listing metadata' -ginkgo.no-color
```
