package git

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
)

// RefKind is what a resolved ref names.
type RefKind string

const (
	RefBranch RefKind = "branch"
	RefTag    RefKind = "tag"
	// RefHead is a remote HEAD that is not a symbolic ref to a branch.
	RefHead RefKind = "head"
	// RefSHA is a full commit SHA given directly.
	RefSHA RefKind = "sha"
)

// Ref is a remote ref resolved to a commit. For an annotated tag SHA is the
// peeled commit and Annotated is set.
type Ref struct {
	Name      string
	FullName  string
	SHA       string
	Kind      RefKind
	Annotated bool
}

// RemoteRefs are the branches and tags a remote advertises, plus its HEAD.
type RemoteRefs struct {
	Head     *Ref
	Branches []Ref
	Tags     []Ref
}

var (
	fullSHAPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	shortSHAPattern = regexp.MustCompile(`^[0-9a-f]{4,39}$`)
)

// Resolve finds name as, in order: a full ref, HEAD, a branch, a tag, then a
// 40-char SHA. A branch takes precedence over a same-named tag. An empty name
// is HEAD. Short SHAs are refused: they cannot be fetched by name.
func (r RemoteRefs) Resolve(name string) (Ref, error) {
	if name == "" || name == "HEAD" {
		if r.Head == nil {
			return Ref{}, fmt.Errorf("remote advertises no HEAD")
		}
		return *r.Head, nil
	}
	if ref, ok := r.findBy(func(ref Ref) bool { return ref.FullName == name }); ok {
		return ref, nil
	}
	branch, isBranch := r.findIn(r.Branches, name)
	tag, isTag := r.findIn(r.Tags, name)
	switch {
	case isBranch:
		return branch, nil
	case isTag:
		return tag, nil
	case fullSHAPattern.MatchString(name):
		return Ref{Name: name, FullName: name, SHA: name, Kind: RefSHA}, nil
	case shortSHAPattern.MatchString(name):
		return Ref{}, fmt.Errorf("ref %q looks like a short SHA: give the full 40-character SHA", name)
	}
	return Ref{}, fmt.Errorf("ref %q not found: no branch or tag of that name", name)
}

func (r RemoteRefs) findBy(match func(Ref) bool) (Ref, bool) {
	for _, ref := range slices.Concat(r.Branches, r.Tags) {
		if match(ref) {
			return ref, true
		}
	}
	return Ref{}, false
}

func (r RemoteRefs) findIn(refs []Ref, name string) (Ref, bool) {
	index := slices.IndexFunc(refs, func(ref Ref) bool { return ref.Name == name })
	if index < 0 {
		return Ref{}, false
	}
	return refs[index], true
}

// refsFromList builds RemoteRefs from a go-git ls-remote listing, peeling
// annotated tags ("refs/tags/x^{}") to their commits.
func refsFromList(list []*plumbing.Reference) RemoteRefs {
	var refs RemoteRefs
	peeled := map[string]string{}
	var head *plumbing.Reference
	for _, ref := range list {
		name := ref.Name().String()
		switch {
		case name == "HEAD":
			head = ref
		case strings.HasSuffix(name, "^{}"):
			peeled[strings.TrimSuffix(name, "^{}")] = ref.Hash().String()
		case ref.Name().IsBranch():
			refs.Branches = append(refs.Branches, Ref{Name: ref.Name().Short(), FullName: name, SHA: ref.Hash().String(), Kind: RefBranch})
		case ref.Name().IsTag():
			refs.Tags = append(refs.Tags, Ref{Name: ref.Name().Short(), FullName: name, SHA: ref.Hash().String(), Kind: RefTag})
		}
	}
	for i, tag := range refs.Tags {
		if commit, ok := peeled[tag.FullName]; ok {
			refs.Tags[i].SHA, refs.Tags[i].Annotated = commit, true
		}
	}
	refs.Head = resolveHead(head, refs.Branches)
	return refs
}

func resolveHead(head *plumbing.Reference, branches []Ref) *Ref {
	if head == nil {
		return nil
	}
	if head.Type() == plumbing.SymbolicReference {
		target := head.Target().String()
		for _, branch := range branches {
			if branch.FullName == target {
				return &branch
			}
		}
		return nil
	}
	return &Ref{Name: "HEAD", FullName: "HEAD", SHA: head.Hash().String(), Kind: RefHead}
}
