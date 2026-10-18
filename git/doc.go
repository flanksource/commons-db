// Package git is commons-db's git client: remote listing, checkouts into a
// shared cache, commits, tags and pushes, and the provider REST APIs (GitHub,
// GitLab, Azure DevOps) that open pull requests.
//
// # Layers
//
//   - url.go parses a repository URL into provider coordinates (RepoURL).
//   - remote.go lists remote refs with go-git (LsRemote) and resolves a ref
//     name to a commit (RemoteRefs.Resolve).
//   - clone.go, cache.go and worktree.go put a ref on disk with the git binary:
//     a plain clone for shell checkouts, or a mirror plus leased worktrees.
//   - change.go composes a worktree into a branch, commit, push and pull
//     request (Cache.Apply).
//   - provider*.go and rest.go talk to the hosting provider's REST API.
//   - factory.go builds a Client from a hydrated models.Connection.
//
// # Why disk operations use the git binary
//
// go-git is used for LsRemote only, against in-memory storage. Fetching with
// go-git breaks on servers that need multi_ack negotiation (Azure DevOps; see
// go-git plumbing/protocol/packp/srvresp.go, the multi_ack TODO), which the
// old client papered over by mutating the process-global
// transport.UnsupportedCapabilities. go-git v5 also has no linked worktrees.
// The git binary (>= 2.31, for GIT_CONFIG_COUNT) handles both, and nothing in
// this package mutates go-git globals: TLS for LsRemote is passed per call.
//
// # Credential policy
//
// Credentials reach the git binary only through its environment, never argv
// and never a config file on disk:
//
//   - HTTP(S): GIT_CONFIG_COUNT sets a URL-scoped http.<url>.extraHeader with
//     the Basic Authorization header, plus credential.helper="" so no helper
//     is consulted or written to.
//   - SSH: GIT_SSH_COMMAND points at a 0600 temporary key with strict
//     known_hosts checking (disabled only by TLS.InsecureSkipVerify).
//   - GIT_TERMINAL_PROMPT=0 so a rejected credential fails instead of
//     prompting.
//
// Repository URLs must not embed a password. Every revision argument is
// preceded by --end-of-options, and branch, tag and revision names are
// validated before they reach a command line.
//
// # Config isolation
//
// Every command runs in an environment built by baseEnv, so the calling
// process's own git setup cannot steer it:
//
//   - GIT_CONFIG_NOSYSTEM=1 and GIT_CONFIG_GLOBAL=/dev/null: neither the
//     system nor the user's global config is read, so no url.insteadOf,
//     credential helper or default identity leaks in.
//   - core.hooksPath=/dev/null: no hook (pre-commit, pre-push, …) runs, from
//     the user's config or from a repository template.
//   - commit.gpgSign=false and tag.gpgSign=false: a commit or tag never waits
//     on a signing key or agent the server does not have.
//   - inherited GIT_DIR, GIT_WORK_TREE, GIT_CONFIG_*, GIT_SSH*, GIT_ASKPASS,
//     GIT_AUTHOR_*, GIT_COMMITTER_* and GIT_TRACE* are dropped; the last keeps
//     a traced Authorization header out of the stderr an error carries.
//
// Commit and tag authors are therefore always explicit (Signature).
// DefaultIdentity is the one deliberate read of the global config: it returns
// the process's own user.name and user.email, for a caller that authors a
// commit as the server itself.
package git
