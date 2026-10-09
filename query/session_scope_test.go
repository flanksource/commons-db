package query_test

import (
	stdcontext "context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// scopeSpecLabel is the record label the scoped registry under test keeps a
// session's environment in.
const scopeSpecLabel = "environment"

type scopeSpecKey struct{}

// inScope is a context naming environment as its scope.
func inScope(environment string) stdcontext.Context {
	return stdcontext.WithValue(stdcontext.Background(), scopeSpecKey{}, environment)
}

func scopeSpecOf(ctx stdcontext.Context) (string, error) {
	environment, ok := ctx.Value(scopeSpecKey{}).(string)
	if !ok {
		return "", errors.New("context names no environment")
	}
	return environment, nil
}

func newScopedRegistry(maxSessions int) *query.SessionRegistry {
	reg := query.NewSessionRegistry(query.RegistryOptions{
		MaxSessions: maxSessions,
		Scope:       &query.SessionScope{Label: scopeSpecLabel, Of: scopeSpecOf},
	})
	DeferCleanup(func() { Expect(reg.StopAll(stdcontext.Background())).To(Succeed()) })
	return reg
}

func trackIn(reg *query.SessionRegistry, ctx stdcontext.Context, labels map[string]string) (*query.Session, error) {
	stopAt := time.Now().Add(time.Minute)
	session, err := reg.Track(ctx, query.TrackOptions{Profile: "trace-capture/scope", Kind: query.KindCapture, Labels: labels, StopAt: &stopAt})
	if err == nil {
		session.OnStop(func(string) { session.Finish(query.FinishUpdate{}) })
	}
	return session, err
}

var _ = Describe("SessionRegistry scope", func() {
	It("labels a tracked session with the scope its start context names", func() {
		reg := newScopedRegistry(5)

		session, err := trackIn(reg, inScope("env-a"), map[string]string{"origin": "spec"})

		Expect(err).ToNot(HaveOccurred())
		Expect(session.Snapshot().Labels).To(Equal(map[string]string{"origin": "spec", scopeSpecLabel: "env-a"}))
	})

	It("refuses a session whose label names another scope than its start context", func() {
		reg := newScopedRegistry(5)

		_, err := trackIn(reg, inScope("env-a"), map[string]string{scopeSpecLabel: "env-b"})

		Expect(err).To(MatchError(ContainSubstring(`"env-b"`)))
		Expect(reg.List()).To(BeEmpty())
	})

	It("refuses a session when its start context names no scope", func() {
		reg := newScopedRegistry(5)

		_, err := trackIn(reg, stdcontext.Background(), nil)

		Expect(err).To(MatchError(ContainSubstring("context names no environment")))
		Expect(reg.List()).To(BeEmpty())
	})

	It("caps active captures per scope, not across the process", func() {
		reg := newScopedRegistry(1)
		_, err := trackIn(reg, inScope("env-a"), nil)
		Expect(err).ToNot(HaveOccurred())

		_, errB := trackIn(reg, inScope("env-b"), nil)
		_, errA := trackIn(reg, inScope("env-a"), nil)

		Expect(errB).ToNot(HaveOccurred(), "env-b has its own cap")
		Expect(errors.Is(errA, query.ErrMaxSessions)).To(BeTrue(), "%v", errA)
		Expect(errA).To(MatchError(ContainSubstring(fmt.Sprintf("%s %q", scopeSpecLabel, "env-a"))))
	})

	It("shows a context only the sessions of its own scope", func() {
		reg := newScopedRegistry(5)
		a, err := trackIn(reg, inScope("env-a"), nil)
		Expect(err).ToNot(HaveOccurred())
		b, err := trackIn(reg, inScope("env-b"), nil)
		Expect(err).ToNot(HaveOccurred())

		visible, err := reg.Visible(inScope("env-b"))

		Expect(err).ToNot(HaveOccurred())
		Expect([]bool{visible(a.Snapshot().SessionRecord), visible(b.Snapshot().SessionRecord)}).To(Equal([]bool{false, true}))
	})

	It("shows every session when the registry has no scope", func() {
		reg := query.NewSessionRegistry(query.RegistryOptions{})
		DeferCleanup(func() { Expect(reg.StopAll(stdcontext.Background())).To(Succeed()) })
		session, err := trackIn(reg, stdcontext.Background(), nil)
		Expect(err).ToNot(HaveOccurred())

		visible, err := reg.Visible(stdcontext.Background())

		Expect(err).ToNot(HaveOccurred())
		Expect(visible(session.Snapshot().SessionRecord)).To(BeTrue())
	})
})
