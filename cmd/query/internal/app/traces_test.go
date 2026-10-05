// Tests for the server's trace plugins: before serve opens them nothing changes,
// and once open their result profiles and index connection join the catalog.

package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/profilestore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing/traces"
)

type beatParams struct{}

type beat struct {
	At time.Time `json:"at"`
}

type beats struct{}

func (beats) Params() beatParams { return beatParams{} }

func (beats) Schema() recordresults.ResultType[beat] {
	return recordresults.ResultType[beat]{Title: "Beats", TimeColumn: "at"}
}

func (beats) Handle(ctx dbcontext.Context, _ beatParams, _ traces.Emitter[beat], _ traces.Records[beat]) error {
	<-ctx.Done()
	return nil
}

func noConnection(dbcontext.Context, string) (*models.Connection, error) { return nil, nil }

func TestTracePluginsChangeNothingBeforeTheyOpen(t *testing.T) {
	var plugins tracePlugins
	files, err := profiles.NewFileStore(t.TempDir())
	require.NoError(t, err)

	require.Nil(t, plugins.current())
	store, err := plugins.overlay(files)
	require.NoError(t, err)
	require.Same(t, files, store)
	release, err := plugins.beforeExecute(context.Background(), []profilestore.ReadRequest{{Profile: query.Profile{Name: "traces/beats"}}})
	require.NoError(t, err)
	release()
	connection, err := plugins.resolveConnection(noConnection)(dbcontext.New(), "connection://traces/traces")
	require.NoError(t, err)
	require.Nil(t, connection)
}

func TestTracePluginsServeTheirResultProfilesOnceOpen(t *testing.T) {
	var plugins tracePlugins
	kinds := traces.NewKinds()
	require.NoError(t, kinds.RegisterKind("beats", traces.NewHandler[beatParams, beat](beats{}, traces.Capabilities{Live: true})))
	runtime, closeTraces, err := plugins.open(t.TempDir(), time.Hour, kinds)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeTraces()) })
	require.Same(t, runtime, plugins.current())

	files, err := profiles.NewFileStore(t.TempDir())
	require.NoError(t, err)
	store, err := plugins.overlay(files)
	require.NoError(t, err)
	listed, err := store.List(context.Background())
	require.NoError(t, err)
	var names []string
	for _, profile := range listed {
		names = append(names, profile.Name)
	}
	require.Contains(t, names, "traces/beats")

	connection, err := plugins.resolveConnection(noConnection)(dbcontext.New(), "connection://traces/traces")
	require.NoError(t, err)
	require.NotNil(t, connection)
}

func TestServeServesTheTraceKinds(t *testing.T) {
	kinds, err := traceKinds()
	require.NoError(t, err)
	infos, err := kinds.List()
	require.NoError(t, err)
	var names []string
	for _, info := range infos {
		names = append(names, info.Name)
	}
	require.Equal(t, []string{"http", "opensearch", "sql", "sql_xevent"}, names)
}
