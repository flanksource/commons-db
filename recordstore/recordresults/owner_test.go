// Specs for how Open shares its files with other processes: the sqlite store
// through an elected owner, ndjson exclusively, and a source's index privately
// when another process holds the shared one.
package recordresults_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/owner"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

var _ = Describe("Open shared with other processes", func() {
	var ctx context.Context

	BeforeEach(func() { ctx = context.Background() })

	options := func(settings recordstore.Settings) recordresults.OpenOptions {
		return recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Build: "test",
			Register: recordresultstest.RegisterSampleEvents,
		}
	}

	It("owns a local sqlite store, publishing its state beside the file", func() {
		settings := recordresultstest.LocalSettings(recordstore.BackendSQLite)
		results := recordresultstest.OpenResults(options(settings))

		Expect(results.Role()).To(Equal(owner.RoleOwner))
		state, found, err := owner.ReadState(filepath.Join(settings.Dir, "records.sqlite"))
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(state.Build).To(Equal("test"))
		Expect(state.PID).To(Equal(os.Getpid()))

		Expect(results.Close()).To(Succeed())
		_, found, err = owner.ReadState(filepath.Join(settings.Dir, "records.sqlite"))
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse(), "closing releases the store")
	})

	It("refuses a second holder of a local ndjson store, naming the first", func() {
		settings := recordresultstest.LocalSettings(recordstore.BackendNDJSON)
		results := recordresultstest.OpenResults(options(settings))
		Expect(results.Role()).To(Equal(owner.RoleOwner))

		_, err := recordresults.Open(options(settings))
		Expect(errors.Is(err, owner.ErrLocked)).To(BeTrue(), "Open: %v", err)
		Expect(err.Error()).To(ContainSubstring("pid"))

		Expect(results.Close()).To(Succeed())
		again := recordresultstest.OpenResults(options(settings))
		Expect(again.Role()).To(Equal(owner.RoleOwner))
	})

	It("indexes a source privately while another holder has its index, removing the private index once free", func() {
		settings := recordresultstest.LocalSettings("")
		open := func() *recordresults.Results {
			sourced := options(settings)
			sourced.Source = recordresultstest.KVRouter(recordstore.NewSchemas())
			results, err := recordresults.Open(sourced)
			Expect(err).ToNot(HaveOccurred())
			return results
		}
		shared := open()
		DeferCleanup(shared.Close)
		private := open()
		Expect(shared.Role()).To(Equal(owner.RoleOwner))
		Expect(private.Role()).To(Equal(owner.RoleReader))
		privateDirs, err := os.ReadDir(filepath.Join(settings.Dir, "private"))
		Expect(err).ToNot(HaveOccurred())
		Expect(privateDirs).To(HaveLen(1))

		Expect(private.Close()).To(Succeed())
		third := open()
		DeferCleanup(third.Close)
		Expect(third.Role()).To(Equal(owner.RoleReader))
		privateDirs, err = os.ReadDir(filepath.Join(settings.Dir, "private"))
		Expect(err).ToNot(HaveOccurred())
		Expect(privateDirs).To(HaveLen(1), "the closed holder's private index was removed; only the new one is left")
	})

	It("opens a route's sqlite file through its owner, closing the hold with the backend", func() {
		path := filepath.Join(GinkgoT().TempDir(), "environments", "prod", "records.sqlite")
		shared, err := recordresults.OpenSharedSQLite(ctx, sqlite.Options{
			Path: path, Schema: recordstoretest.Schema, TTL: time.Hour, SweepInterval: time.Hour,
		}, "test")
		Expect(err).ToNot(HaveOccurred())
		var backend recordstore.Backend = shared
		Expect(backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))).Error().ToNot(HaveOccurred())
		Expect(shared.Role()).To(Equal(owner.RoleOwner))

		Expect(backend.Close()).To(Succeed())
		_, found, err := owner.ReadState(path)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})
