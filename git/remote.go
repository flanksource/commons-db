package git

import (
	"context"
	"fmt"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons/http/middlewares/timing"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Repository is one remote repository with the credentials to reach it and,
// when it is hosted on a supported provider, that provider's API.
type Repository struct {
	URL  RepoURL
	Auth Auth
	// Provider opens pull requests; nil or a generic git provider has none.
	Provider Provider
}

// RepositoryOptions configures NewRepository.
type RepositoryOptions struct {
	URL      string
	Auth     Auth
	Provider Provider
	// ProviderType is the URL parsing hint (models.ConnectionType*); it
	// defaults to Provider.Type().
	ProviderType string
}

// NewRepository parses the URL and validates the credentials.
func NewRepository(options RepositoryOptions) (*Repository, error) {
	hint := options.ProviderType
	if hint == "" && options.Provider != nil {
		hint = options.Provider.Type()
	}
	parsed, err := ParseRepoURL(options.URL, hint)
	if err != nil {
		return nil, err
	}
	if err := options.Auth.Validate(); err != nil {
		return nil, fmt.Errorf("repository %s: %w", parsed.Redacted(), err)
	}
	return &Repository{URL: parsed, Auth: options.Auth, Provider: options.Provider}, nil
}

// LsRemote lists the remote's refs with go-git against in-memory storage;
// TLS settings are passed per call. A local repository is listed by the git
// binary (lsRemoteLocal).
func (r *Repository) LsRemote(ctx context.Context) (refs RemoteRefs, err error) {
	if path, local := r.URL.LocalPath(); local {
		return lsRemoteLocal(ctx, path)
	}
	finish := startGitTiming(ctx, "ls_remote")
	defer func() { finish(err, "", nil) }()
	ctx = timing.WithClientTimings(ctx)
	auth, err := r.Auth.goGitAuth(r.URL)
	if err != nil {
		return RemoteRefs{}, fmt.Errorf("ls-remote %s: %w", r.URL.Redacted(), err)
	}
	remote := gogit.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{r.URL.Clone}})
	list, err := remote.ListContext(ctx, &gogit.ListOptions{
		Auth:            auth,
		PeelingOption:   gogit.AppendPeeled,
		InsecureSkipTLS: r.Auth.TLS.InsecureSkipVerify,
		CABundle:        []byte(r.Auth.TLS.CA),
		ClientCert:      []byte(r.Auth.TLS.Cert),
		ClientKey:       []byte(r.Auth.TLS.Key),
	})
	if err != nil {
		return RemoteRefs{}, fmt.Errorf("ls-remote %s: %s", r.URL.Redacted(), Redact(err.Error()))
	}
	return refsFromList(list), nil
}

// Resolve lists the remote and resolves ref (see RemoteRefs.Resolve); an
// empty ref is the remote HEAD. A full 40-character SHA is the commit itself,
// without listing the remote, even if a branch or tag carries that name.
func (r *Repository) Resolve(ctx context.Context, ref string) (Ref, error) {
	if fullSHAPattern.MatchString(ref) {
		return Ref{Name: ref, FullName: ref, SHA: ref, Kind: RefSHA}, nil
	}
	refs, err := r.LsRemote(ctx)
	if err != nil {
		return Ref{}, err
	}
	resolved, err := refs.Resolve(ref)
	if err != nil {
		return Ref{}, fmt.Errorf("%s: %w", r.URL.Redacted(), err)
	}
	return resolved, nil
}

// originRemote is the namespace of the repository a mirror belongs to.
const originRemote = "origin"

// refspec fetches ref so that FETCH_HEAD names it. From origin, branches land
// under refs/remotes/origin and tags under refs/tags of the local repository;
// from any other remote both land under refs/remotes/<remote>, never over the
// local repository's own refs.
func (ref Ref) refspec(remote string) string {
	switch ref.Kind {
	case RefBranch:
		return "+" + ref.FullName + ":refs/remotes/" + remote + "/" + ref.Name
	case RefTag:
		if remote != originRemote {
			return "+" + ref.FullName + ":refs/remotes/" + remote + "/tags/" + ref.Name
		}
		return "+" + ref.FullName + ":" + ref.FullName
	case RefHead:
		return "HEAD"
	default:
		return ref.SHA
	}
}

// remoteOf is the namespace repo's refs take in owner's mirror: origin for
// owner itself, repo's cache key for a foreign repository (e.g. a fork).
func remoteOf(owner, repo *Repository) string {
	if repo == owner || repo.URL.Clone == owner.URL.Clone {
		return originRemote
	}
	return repo.URL.CacheKey()
}

// fetchOptions configures fetch: Ref is fetched from Repo into the repository
// at Dir, the mirror of Owner. Depth > 0 shallows the fetch; Flags are extra
// fetch options (e.g. --unshallow).
type fetchOptions struct {
	Refspec string
	Dir     string
	Owner   *Repository
	Repo    *Repository
	Ref     Ref
	Depth   int
	Flags   []string
}

// fetch fetches options.Ref and returns the fetched commit, which may be
// newer than the SHA ls-remote reported.
func fetch(ctx context.Context, options fetchOptions) (sha string, err error) {
	repo, ref := options.Repo, options.Ref
	env, cleanup, err := commandEnv(repo)
	if err != nil {
		return "", err
	}
	defer cleanup()
	verbosity, report := "--quiet", func(FetchProgress) {}
	if observer := fetchObserver(ctx); observer != nil {
		verbosity = "--progress"
		var received string
		report = func(progress FetchProgress) {
			if progress.Phase == "Receiving objects" && progress.Transferred != "" {
				received = progress.Transferred
			}
			progress.Repository, progress.Ref = repo.URL.Redacted(), ref.Name
			observer(progress)
		}
		defer func() {
			if kib, ok := transferredKiB(received); ok {
				rpchttp.AddTiming(ctx, rpchttp.TimingMetric{Name: "git_fetch", Counters: []rpchttp.TimingCounter{{Name: "received_kib", Value: kib}}})
			}
			report(FetchProgress{Done: true, Err: err})
		}()
	}
	args := append([]string{"fetch", verbosity, "--no-tags"}, options.Flags...)
	if options.Depth > 0 {
		args = append(args, fmt.Sprintf("--depth=%d", options.Depth))
	}
	refspec := options.Refspec
	if refspec == "" {
		refspec = ref.refspec(remoteOf(options.Owner, repo))
	}
	args = append(args, "--end-of-options", repo.URL.Clone, refspec)
	if _, err := run(ctx, command{dir: options.Dir, env: env, args: args, progress: report}); err != nil {
		return "", fmt.Errorf("fetch %s from %s: %w", ref.Name, repo.URL.Redacted(), err)
	}
	return run(ctx, command{dir: options.Dir, args: []string{"rev-parse", "--verify", "FETCH_HEAD^{commit}"}})
}
