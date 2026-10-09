package query

import (
	stdcontext "context"
	"errors"
	"fmt"
	"maps"
)

// SessionScope partitions one registry's sessions when a process serves
// several scopes — environments, tenants — at once. Every session the registry
// starts is labelled with the scope its start context names, the MaxSessions
// and MaxViews caps count per scope, and Visible shows a context only the
// sessions of its own scope. A Store the registry writes to must partition its
// records by the same scope: the registry scopes only the sessions it holds.
type SessionScope struct {
	// Label is the record label a session's scope is kept in.
	Label string

	// Of names the scope ctx operates in. An error, or an empty scope,
	// refuses whatever ctx was asking for.
	Of func(ctx stdcontext.Context) (string, error)
}

func (s *SessionScope) validate() error {
	switch {
	case s == nil:
		return nil
	case s.Label == "":
		return errors.New("session scope: a label is required")
	case s.Of == nil:
		return fmt.Errorf("session scope %q: a scope resolver is required", s.Label)
	}
	return nil
}

// scopeOf is the scope ctx names. RegistryOptions.Scope must be set.
func (r *SessionRegistry) scopeOf(ctx stdcontext.Context) (string, error) {
	scope, err := r.opts.Scope.Of(ctx)
	switch {
	case err != nil:
		return "", fmt.Errorf("session scope %s: %w", r.opts.Scope.Label, err)
	case scope == "":
		return "", fmt.Errorf("session scope %s: the context names an empty scope", r.opts.Scope.Label)
	}
	return scope, nil
}

// scopeLabels is labels with the scope ctx names added, for a session starting
// under ctx. Labels already naming another scope are refused. Without a Scope
// labels are returned as they are.
func (r *SessionRegistry) scopeLabels(ctx stdcontext.Context, labels map[string]string) (map[string]string, error) {
	if r.opts.Scope == nil {
		return labels, nil
	}
	scope, err := r.scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	label := r.opts.Scope.Label
	if current, ok := labels[label]; ok && current != scope {
		return nil, fmt.Errorf("session label %s %q is not the scope %q its start context names", label, current, scope)
	}
	scoped := maps.Clone(labels)
	if scoped == nil {
		scoped = map[string]string{}
	}
	scoped[label] = scope
	return scoped, nil
}

// Visible reports which records the operation ctx carries may see: those
// labelled with the scope it names, or every record when the registry has no
// Scope.
func (r *SessionRegistry) Visible(ctx stdcontext.Context) (func(SessionRecord) bool, error) {
	if r.opts.Scope == nil {
		return func(SessionRecord) bool { return true }, nil
	}
	scope, err := r.scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	label := r.opts.Scope.Label
	return func(rec SessionRecord) bool { return rec.Labels[label] == scope }, nil
}

// admissionLocked refuses a session of role carrying labels when its cap —
// MaxViews for a view, MaxSessions for a capture — is already reached among
// the active sessions of its scope (of the whole registry without a Scope).
// Registry.mu must be held.
func (r *SessionRegistry) admissionLocked(role SessionRole, labels map[string]string) error {
	limit, refusal := r.opts.MaxSessions, ErrMaxSessions
	if role == SessionRoleView {
		limit, refusal = r.opts.MaxViews, ErrMaxViews
	}
	if r.opts.Scope == nil {
		if active := r.activeLocked(role); active >= limit {
			return fmt.Errorf("%w (%d active); stop one first", refusal, active)
		}
		return nil
	}
	label := r.opts.Scope.Label
	scope, ok := labels[label]
	if !ok || scope == "" {
		return fmt.Errorf("session has no %s label; a scoped registry admits only sessions it labelled", label)
	}
	active := 0
	for _, s := range r.sessions {
		if info := s.Snapshot(); !info.State.Terminal() && info.Role == role && info.Labels[label] == scope {
			active++
		}
	}
	if active >= limit {
		return fmt.Errorf("%w (%d active in %s %q); stop one first", refusal, active, label, scope)
	}
	return nil
}
