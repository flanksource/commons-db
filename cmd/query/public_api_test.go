package main_test

import (
	"context"
	"testing"

	"github.com/flanksource/commons-db/cmd/query/connections"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/sessions"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPublicPackages(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Query module public packages")
}

var _ profiles.Store = (*profiles.FileStore)(nil)
var _ profiles.Store = (*profiles.DBStore)(nil)

type profileReader interface {
	Get(context.Context, string) (query.Profile, error)
}

var _ profileReader = (*profiles.Service)(nil)

type connectionLibrary interface {
	List(connections.ListOptions) ([]*models.Connection, error)
	Get(string) (*models.Connection, error)
	Create(context.Context, map[string]any) (*models.Connection, error)
	Update(context.Context, string, map[string]any) (*models.Connection, error)
	Delete(string) error
}

var _ connectionLibrary = (*connections.Service)(nil)

type profileLibrary interface {
	List(context.Context) ([]query.Profile, error)
	Get(context.Context, string) (query.Profile, error)
	Save(context.Context, map[string]any, string) (query.Profile, error)
	Delete(context.Context, string) error
}

var _ profileLibrary = (*profiles.Service)(nil)

type sessionLibrary interface {
	RunTrace(context.Context, string, sessions.TraceOptions) error
	RunTop(context.Context, string, sessions.TopOptions) error
}

var _ sessionLibrary = (*sessions.Runner)(nil)

var _ = Describe("public constructors", func() {
	It("rejects incomplete dependencies", func() {
		_, err := connections.New(connections.Options{})
		Expect(err).To(MatchError(ContainSubstring("database provider")))

		_, err = profiles.New(profiles.Options{})
		Expect(err).To(MatchError(ContainSubstring("store provider")))

		_, err = sessions.New(sessions.Options{})
		Expect(err).To(MatchError(ContainSubstring("profile store provider")))
	})
})
