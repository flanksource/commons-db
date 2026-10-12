// Specs for the kinds catalog: registering trace plugins, describing their
// params form and columns, validating params and declaring their result types.

package traces_test

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
)

// stopped stops session, returning it.
func stopped(session *query.Session) *query.Session {
	session.Stop("spec")
	return session
}

type untitled struct{ ticker }

func (untitled) Schema() recordresults.ResultType[tick] { return recordresults.ResultType[tick]{} }

type labelParams struct {
	Labels []string `json:"labels,omitempty"`
}

// labeller emits one tick per label its params name, then ends.
type labeller struct{}

func (labeller) Params() labelParams { return labelParams{} }

func (labeller) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Labels", TimeColumn: "at"}
}

func (labeller) Handle(ctx dbcontext.Context, p labelParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
	for n, label := range p.Labels {
		if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: n + 1, Label: label}); err != nil {
			return err
		}
	}
	return nil
}

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

	It("prefills a kind's form and starts its captures from the defaults a host gives it", func() {
		plugin := traces.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, p tickParams, records traces.Emitter[tick], _ traces.Records[tick]) error {
			for n := 1; n <= p.Count; n++ {
				if err := records.Emit(ctx, tick{At: time.Now().UTC(), N: n, Label: p.Label}); err != nil {
					return err
				}
			}
			<-ctx.Done()
			return nil
		}}, traces.Capabilities{Live: true}).WithDefaults(tickParams{Count: 7, Label: "host"})
		schema, err := plugin.Params()
		Expect(err).ToNot(HaveOccurred())
		Expect(schema.Properties["count"].Default).To(Equal(float64(7)))
		Expect(schema.Properties["label"].Default).To(Equal("host"))

		env := tracestest.NewEnv(map[string]traces.TracePlugin{"ticks": plugin})
		session := env.Start("ticks", `{"label": "mine"}`)
		Eventually(func() int64 { return session.Snapshot().EventCount }).Should(Equal(int64(7)))
		rows := env.Rows(tracestest.Ended(stopped(session)))
		Expect(rows[0]["label"]).To(Equal("mine"))
	})

	It("starts every capture from the host's defaults, whatever an earlier start asked for", func() {
		plugin := traces.NewHandler[labelParams, tick](labeller{}, traces.Capabilities{Historical: true}).
			WithDefaults(labelParams{Labels: []string{"self"}})
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"labels": plugin})

		asked := env.Rows(tracestest.Ended(env.Start("labels", `{"labels": ["other"]}`)))
		Expect(asked).To(HaveLen(1))
		Expect(asked[0]["label"]).To(Equal("other"))

		defaulted := env.Rows(tracestest.Ended(env.Start("labels", `{}`)))
		Expect(defaulted).To(HaveLen(1))
		Expect(defaulted[0]["label"]).To(Equal("self"))
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
