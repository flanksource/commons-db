package git

import (
	"context"
	"fmt"

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
// TLS settings are passed per call.
func (r *Repository) LsRemote(ctx context.Context) (RemoteRefs, error) {
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
// empty ref is the remote HEAD.
func (r *Repository) Resolve(ctx context.Context, ref string) (Ref, error) {
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

// refspec fetches ref so that FETCH_HEAD names it; branches land under
// refs/remotes/origin and tags under refs/tags of the local repository.
func (ref Ref) refspec() string {
	switch ref.Kind {
	case RefBranch:
		return "+" + ref.FullName + ":refs/remotes/origin/" + ref.Name
	case RefTag:
		return "+" + ref.FullName + ":" + ref.FullName
	case RefHead:
		return "HEAD"
	default:
		return ref.SHA
	}
}

// fetch fetches ref from repo into the repository at dir and returns the
// fetched commit, which may be newer than the SHA ls-remote reported.
func fetch(ctx context.Context, dir string, repo *Repository, ref Ref, depth int) (string, error) {
	env, cleanup, err := commandEnv(repo)
	if err != nil {
		return "", err
	}
	defer cleanup()
	args := []string{"fetch", "--quiet", "--no-tags"}
	if depth > 0 {
		args = append(args, fmt.Sprintf("--depth=%d", depth))
	}
	args = append(args, "--end-of-options", repo.URL.Clone, ref.refspec())
	if _, err := run(ctx, command{dir: dir, env: env, args: args}); err != nil {
		return "", fmt.Errorf("fetch %s from %s: %w", ref.Name, repo.URL.Redacted(), err)
	}
	return run(ctx, command{dir: dir, args: []string{"rev-parse", "--verify", "FETCH_HEAD^{commit}"}})
}
