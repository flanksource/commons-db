// Specs for the kinds catalog: registering trace plugins, describing their
// params form and columns, validating params and declaring their result types.

package traces_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"

	"github.com/flanksource/commons-db/tracing/traces"
)

type untitled struct{ ticker }

func (untitled) Schema() recordresults.ResultType[tick] { return recordresults.ResultType[tick]{} }

var _ = Describe("Kinds", func() {
	It("describes a registered kind: its params form with defaults, its columns and capabilities", func() {
		kinds := traces.NewKinds()
		Expect(kinds.RegisterKind("ticks", tickPlugin())).To(Succeed())

		infos, err := kinds.List()
		Expect(err).ToNot(HaveOccurred())
		Expect(infos).To(HaveLen(1))
		info := infos[0]
		Expect(info.Name).To(Equal("ticks"))
		Expect(info.Title).To(Equal("Ticks"))
		Expect(info.Capabilities).To(Equal(traces.Capabilities{Live: true}))

		raw, err := json.Marshal(info.Params)
		Expect(err).ToNot(HaveOccurred())
		Expect(raw).To(MatchJSON(`{
			"type": "object",
			"properties": {
				"count": {"type": "integer", "format": "int32", "default": 3},
				"label": {"type": "string"}
			},
			"required": ["count"]
		}`))

		Expect(info.Columns).To(HaveLen(3))
		Expect(info.Columns[0]).To(Equal(query.ColumnDef{
			Name: "at", Label: "At", Type: query.ColumnTypeDateTime, Kind: query.ColumnKindTimestamp,
		}))
		Expect(info.Columns[1].Name).To(Equal("n"))
		Expect(info.Columns[2].Name).To(Equal("label"))
	})

	It("lists kinds by name", func() {
		kinds := traces.NewKinds()
		Expect(kinds.RegisterKind("zeta", tickPlugin())).To(Succeed())
		Expect(kinds.RegisterKind("alpha", tickPlugin())).To(Succeed())
		infos, err := kinds.List()
		Expect(err).ToNot(HaveOccurred())
		Expect(infos).To(HaveLen(2))
		Expect(infos[0].Name).To(Equal("alpha"))
		Expect(infos[1].Name).To(Equal("zeta"))
	})

	It("finds a registered kind by name", func() {
		kinds := traces.NewKinds()
		plugin := tickPlugin()
		Expect(kinds.RegisterKind("ticks", plugin)).To(Succeed())
		found, ok := kinds.Get("ticks")
		Expect(ok).To(BeTrue())
		Expect(found).To(BeIdenticalTo(plugin))
		_, ok = kinds.Get("other")
		Expect(ok).To(BeFalse())
	})

	DescribeTable("refuses a kind it could not serve",
		func(register func(*traces.Kinds) error, message string) {
			Expect(register(traces.NewKinds())).To(MatchError(ContainSubstring(message)))
		},
		Entry("a name a record store kind cannot have", func(k *traces.Kinds) error {
			return k.RegisterKind("tick-tock", tickPlugin())
		}, "tick-tock"),
		Entry("a name registered twice", func(k *traces.Kinds) error {
			Expect(k.RegisterKind("ticks", tickPlugin())).To(Succeed())
			return k.RegisterKind("ticks", tickPlugin())
		}, "already registered"),
		Entry("no capabilities", func(k *traces.Kinds) error {
			return k.RegisterKind("ticks", traces.NewHandler[tickParams, tick](ticker{}, traces.Capabilities{}))
		}, "capabilities"),
		Entry("no title", func(k *traces.Kinds) error {
			return k.RegisterKind("ticks", traces.NewHandler[tickParams, tick](untitled{}, traces.Capabilities{Live: true}))
		}, "title"),
	)

	DescribeTable("validates params as a session start would decode them",
		func(params string, message string) {
			err := tickPlugin().ValidateParams(json.RawMessage(params))
			if message == "" {
				Expect(err).ToNot(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("no params", "", ""),
		Entry("null params", "null", ""),
		Entry("known fields", `{"count": 2, "label": "x"}`, ""),
		Entry("an unknown field", `{"cnt": 2}`, `"cnt"`),
		Entry("a field of the wrong type", `{"count": "two"}`, "count"),
		Entry("trailing data", `{"count": 2} {}`, "trailing"),
		Entry("params the type's Validate refuses", `{"count": -1}`, "must not be negative"),
	)

	It("declares every kind's result type into the results store it opens with", func() {
		kinds := traces.NewKinds()
		Expect(kinds.RegisterKind("ticks", tickPlugin())).To(Succeed())

		results := recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "traces", ConnectionName: "traces",
			Settings: recordresultstest.LocalSettings(recordstore.BackendSQLite),
			Register: kinds.RegisterResultTypes,
		})
		profile, err := results.Registry.Get(context.Background(), "traces/ticks")
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Name).To(Equal("traces/ticks"))
		Expect(results.Registry.ResultTypes()).To(ContainElement(And(
			HaveField("Kind", "ticks"), HaveField("Title", "Ticks"), HaveField("Profile", "traces/ticks"),
		)))
	})
})
