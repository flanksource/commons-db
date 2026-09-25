// Specs for Open that need no HTTP surface: what it closes, what it refuses,
// and where it declares result types and puts its files.
package recordresults_test

import (
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/cmd/query/recordresults/recordresultstest"
	"github.com/flanksource/commons-db/recordstore"
)

var _ = Describe("Open", func() {
	It("closes what it opened, the caller's source included", func() {
		router := recordresultstest.KVRouter(recordstore.NewSchemas())
		results, err := recordresults.Open(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(""), Source: router,
			Register: recordresultstest.RegisterSampleEvents,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(results.Close()).To(Succeed())
		_, err = results.Backend.Meta(recordresultstest.ForTenant("a"), "run-1")
		Expect(err).To(MatchError(ContainSubstring("closed")))
	})

	DescribeTable("refuses options it cannot open rather than guessing",
		func(mutate func(*recordresults.OpenOptions), message string) {
			options := recordresults.OpenOptions{
				Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(recordstore.BackendSQLite),
				Register: recordresultstest.RegisterSampleEvents,
			}
			mutate(&options)
			_, err := recordresults.Open(options)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a kv backend with no source to route it", func(o *recordresults.OpenOptions) {
			o.Settings.Backend = recordstore.BackendKV
		}, "Router"),
		Entry("an unresolved backend with no source", func(o *recordresults.OpenOptions) {
			o.Settings.Backend = ""
		}, "Resolve"),
		Entry("no directory", func(o *recordresults.OpenOptions) { o.Settings.Dir = "" }, "directory"),
		Entry("no ttl", func(o *recordresults.OpenOptions) { o.Settings.TTL = 0 }, "ttl"),
		Entry("no result types", func(o *recordresults.OpenOptions) { o.Register = nil }, "Register"),
		Entry("a failing registration", func(o *recordresults.OpenOptions) {
			o.Register = func(*recordresults.Registry) error { return errors.New("bad result type") }
		}, "bad result type"),
	)

	It("closes the caller's source when it fails to open", func() {
		router := recordresultstest.KVRouter(recordstore.NewSchemas())
		_, err := recordresults.Open(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(""), Source: router,
			Register: func(*recordresults.Registry) error { return errors.New("bad result type") },
		})
		Expect(err).To(HaveOccurred())
		_, err = router.Meta(recordresultstest.ForTenant("a"), "run-1")
		Expect(err).To(MatchError(ContainSubstring("closed")))
	})

	It("declares result types into the caller's schemas, so a store the caller opens can hold them", func() {
		schemas := recordstore.NewSchemas()
		recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(recordstore.BackendSQLite),
			Schemas: schemas, Register: recordresultstest.RegisterSampleEvents,
		})
		schema, err := schemas.Kind("sample_event")
		Expect(err).ToNot(HaveOccurred())
		Expect(schema.Columns).To(ContainElement(HaveField("Name", "db")))
	})

	It("creates the directory the files go in", func() {
		settings := recordresultstest.LocalSettings(recordstore.BackendSQLite)
		settings.Dir = filepath.Join(settings.Dir, "not", "yet")
		recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Register: recordresultstest.RegisterSampleEvents,
		})
		info, err := os.Stat(settings.Dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.IsDir()).To(BeTrue())
	})
})
