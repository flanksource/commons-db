package query_test

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

// Both sides carry the same identities under different field names, so the join
// needs one expression that reads either shape — the case ReconcileProfiles
// exists for.
func reconcileRows(prefix, field string, count int) []query.Row {
	rows := make([]query.Row, 0, count)
	for i := 1; i <= count; i++ {
		rows = append(rows, query.Row{field: fmt.Sprintf("%s%03d", prefix, i)})
	}
	return rows
}

func reconcileProfile(name, providerType string) query.Profile {
	return query.Profile{Name: name, Provider: query.ProviderConfig{Type: providerType}}
}

// orderedProfile names the key column as its order, which is what lets the two
// sides be merged rather than both read in full.
func orderedProfile(name, providerType, keyColumn string) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: providerType},
		Order:    query.Order{{Column: keyColumn, Unique: true}},
	}
}

// mergeRunFor runs a merge join over two ordered sides keyed on their shared
// order column — the shape that lets both be walked rather than buffered.
func mergeRunFor(keys *query.KeyRange, sourceRows, destRows int) *query.ReconcileResult {
	query.RegisterProvider(&mockProvider{typ: "merge-source", rows: reconcileRows("ord", "order_id", sourceRows)})
	query.RegisterProvider(&mockProvider{typ: "merge-dest", rows: reconcileRows("ord", "order_id", destRows)})

	result, err := query.ReconcileProfiles(reconcileCtx(), query.ReconcileRun{
		Source: orderedProfile("orders-emitted", "merge-source", "order_id"),
		Dest:   orderedProfile("orders-ingested", "merge-dest", "order_id"),
		Config: query.ReconcileConfig{
			Dest: "orders-ingested",
			ReconcileSpec: query.ReconcileSpec{
				Range: keys,
				Key:   query.KeySpec{Columns: []string{"order_id"}},
			},
		},
	})
	Expect(err).ToNot(HaveOccurred())
	return result
}

var _ = Describe("ReconcileProfiles", func() {
	const keyCEL = `has(row.order_id) ? string(row.order_id) : string(row.order_ref)`

	// A CEL key can be read off a row but not ordered by, so a run using one is
	// joined by reading both sides in full.
	run := func(sourceRows, destRows int) *query.ReconcileResult {
		query.RegisterProvider(&mockProvider{typ: "recon-source", rows: reconcileRows("ord", "order_id", sourceRows)})
		query.RegisterProvider(&mockProvider{typ: "recon-dest", rows: reconcileRows("ord", "order_ref", destRows)})

		result, err := query.ReconcileProfiles(reconcileCtx(), query.ReconcileRun{
			Source: reconcileProfile("orders-emitted", "recon-source"),
			Dest:   reconcileProfile("orders-ingested", "recon-dest"),
			Config: query.ReconcileConfig{
				Dest:          "orders-ingested",
				ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{CEL: keyCEL}},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		return result
	}

	It("runs both profiles and joins them on the shared key", func() {
		result := run(4, 4)

		Expect(result.Source).To(Equal("orders-emitted"))
		Expect(result.Dest).To(Equal("orders-ingested"))
		Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: 4}))
		Expect(result.Bounded()).To(BeFalse())
	})

	It("passes different values for the same filter name to each side", func() {
		sourceProvider := &mockProvider{typ: "recon-filter-source", rows: []query.Row{{"id": "ord001"}}}
		destProvider := &mockProvider{typ: "recon-filter-dest", rows: []query.Row{{"id": "ord001"}}}
		query.RegisterProvider(sourceProvider)
		query.RegisterProvider(destProvider)
		source := reconcileProfile("orders-emitted", sourceProvider.typ)
		dest := reconcileProfile("orders-ingested", destProvider.typ)
		source.Params = []query.ParamDef{{Name: "region"}}
		dest.Params = []query.ParamDef{{Name: "region"}}

		result, err := query.ReconcileProfiles(reconcileCtx(), query.ReconcileRun{
			Source:        source,
			Dest:          dest,
			Config:        query.ReconcileConfig{ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{CEL: "row.id"}}},
			SourceFilters: map[string]any{"region": "eu"},
			DestFilters:   map[string]any{"region": "us"},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: 1}))
		Expect(sourceProvider.last.Params).To(HaveKeyWithValue("region", "eu"))
		Expect(destProvider.last.Params).To(HaveKeyWithValue("region", "us"))
	})

	It("says it buffered a CEL-keyed run, and why", func() {
		result := run(2, 2)
		Expect(result.Mode).To(Equal(query.ReconcileBuffered))
		Expect(result.BufferedReason).To(ContainSubstring("CEL expression"))
	})

	It("says nothing about bounds when both sides were read in full", func() {
		Expect(run(2, 2).Pretty().String()).ToNot(ContainSubstring("stopped short"))
	})

	It("fails loudly when a side cannot run", func() {
		query.RegisterProvider(&mockProvider{typ: "recon-source", rows: reconcileRows("ord", "order_id", 1)})

		_, err := query.ReconcileProfiles(reconcileCtx(), query.ReconcileRun{
			Source: reconcileProfile("orders-emitted", "recon-source"),
			Dest:   reconcileProfile("orders-ingested", "provider-that-is-not-registered"),
			Config: query.ReconcileConfig{Dest: "orders-ingested", ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{CEL: keyCEL}}},
		})
		Expect(err).To(MatchError(ContainSubstring(`dest profile "orders-ingested"`)))
	})

	Describe("merge join", func() {
		mergeRun := mergeRunFor

		It("merges when the key is the order", func() {
			result := mergeRun(nil, 4, 4)
			Expect(result.Mode).To(Equal(query.ReconcileMerged))
			Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: 4}))
		})

		It("finds the keys one side is missing", func() {
			result := mergeRun(nil, 5, 3)
			Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: 3, OnlySource: 2}))
		})

		// A key range cuts both sides at the same keys, so unlike a per-side row
		// cap it cannot turn a matched key into a one-sided one.
		It("covers exactly the keys in the range", func() {
			result := mergeRun(&query.KeyRange{From: "ord002", To: "ord004"}, 5, 5)
			Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: 2}))
			for _, row := range result.Rows {
				Expect(row.Key).To(BeElementOf("ord002", "ord003"))
			}
		})

		It("reports the range it covered", func() {
			keys := &query.KeyRange{From: "ord002", To: "ord004"}
			Expect(mergeRun(keys, 5, 5).Config.Range).To(Equal(keys))
			Expect(keys.String()).To(Equal("keys from ord002 up to ord004"))
		})

		// The bound that used to manufacture findings: two sides cut at N rows
		// each are two different key sets unless they happen to be identical.
		// A range narrows both sides to the same keys, so a lopsided pair of
		// datasets still reconciles cleanly inside it.
		It("does not invent one-sided keys when the sides differ in size", func() {
			result := mergeRun(&query.KeyRange{From: "ord001", To: "ord003"}, 2, 20)
			Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: 2}))
		})

		It("refuses a range that covers no keys", func() {
			Expect((&query.KeyRange{From: "b", To: "a"}).Validate()).To(
				MatchError(ContainSubstring("covers no keys")))
		})
	})

	// A reconciliation's findings are only as trustworthy as the two reads
	// behind them, and neither read is visible in the joined rows.
	Describe("provenance", func() {
		// One sink is keyed on the context and every recorder is last-write-wins,
		// so a shared sink would report the destination's query for both sides.
		// This is the assertion that the two sides are recorded separately.
		It("records each side's own rendered query", func() {
			query.RegisterProvider(&mockProvider{typ: "prov-source", rows: reconcileRows("ord", "order_id", 2)})
			query.RegisterProvider(&mockProvider{typ: "prov-dest", rows: reconcileRows("ord", "order_ref", 2)})
			source := reconcileProfile("orders-emitted", "prov-source")
			source.Query = "select * from emitted where region = '{{.params.region}}'"
			source.Params = []query.ParamDef{{Name: "region"}}
			dest := reconcileProfile("orders-ingested", "prov-dest")
			dest.Query = "select * from ingested where region = '{{.params.region}}'"
			dest.Params = []query.ParamDef{{Name: "region"}}

			result, err := query.ReconcileProfiles(reconcileCtx(), query.ReconcileRun{
				Source: source, Dest: dest,
				Config:        query.ReconcileConfig{ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{CEL: keyCEL}}},
				SourceFilters: map[string]any{"region": "eu"},
				DestFilters:   map[string]any{"region": "us"},
			})
			Expect(err).ToNot(HaveOccurred())

			provenance := result.Provenance
			Expect(provenance).ToNot(BeNil())
			Expect(provenance.Mode).To(Equal(query.ReconcileBuffered))
			Expect(provenance.Source.Profile).To(Equal("orders-emitted"))
			Expect(provenance.Dest.Profile).To(Equal("orders-ingested"))
			Expect(provenance.Source.Diagnostics.Request.Rendered).To(
				Equal("select * from emitted where region = 'eu'"))
			Expect(provenance.Dest.Diagnostics.Request.Rendered).To(
				Equal("select * from ingested where region = 'us'"))
			Expect(provenance.Source.Diagnostics.Request.Rendered).ToNot(
				Equal(provenance.Dest.Diagnostics.Request.Rendered))

			// The query as authored survives beside what it rendered to, so a
			// reader can tell a bad template from a bad parameter.
			Expect(provenance.Source.Query).To(ContainSubstring("{{.params.region}}"))
			Expect(provenance.Source.Filters).To(Equal(map[string]any{"region": "eu"}))
			Expect(provenance.Source.Rows).To(Equal(2))
		})

		It("records both sides of a merged run", func() {
			result := mergeRunFor(nil, 5, 3)

			Expect(result.Provenance.Mode).To(Equal(query.ReconcileMerged))
			Expect(result.Provenance.Source.Rows).To(Equal(5))
			Expect(result.Provenance.Dest.Rows).To(Equal(3))
		})

		// A range stops the walk early, so what the join consumed is less than
		// what the provider returned — the two counts are not the same fact.
		It("counts what the join consumed, not what the provider returned", func() {
			result := mergeRunFor(&query.KeyRange{From: "ord001", To: "ord003"}, 20, 20)

			Expect(result.Provenance.Source.Rows).To(BeNumerically("<", 20))
		})

		It("keeps credentials out of what it stores", func() {
			query.RegisterProvider(&mockProvider{typ: "prov-secret", rows: reconcileRows("ord", "order_id", 1)})
			secret := reconcileProfile("orders-emitted", "prov-secret")
			secret.Provider.Options = map[string]any{
				"password": "hunter2",
				"url":      "postgres://reader:hunter2@db.example.com:5432/analytics",
			}

			result, err := query.ReconcileProfiles(reconcileCtx(), query.ReconcileRun{
				Source: secret, Dest: secret,
				Config: query.ReconcileConfig{ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{CEL: "row.order_id"}}},
			})
			Expect(err).ToNot(HaveOccurred())

			encoded, err := json.Marshal(result.Provenance)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(encoded)).ToNot(ContainSubstring("hunter2"))
		})
	})

	Describe("Mergeable", func() {
		It("refuses a profile ordered by something other than the key", func() {
			run := query.ReconcileRun{
				Source: orderedProfile("a", "merge-source", "created_at"),
				Dest:   orderedProfile("b", "merge-dest", "order_id"),
				Config: query.ReconcileConfig{ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{Columns: []string{"order_id"}}}},
			}
			mergeable, why := run.Mergeable()
			Expect(mergeable).To(BeFalse())
			Expect(why).To(ContainSubstring("does not begin with the key"))
		})

		It("refuses a profile with no declared order", func() {
			run := query.ReconcileRun{
				Source: reconcileProfile("a", "merge-source"),
				Dest:   orderedProfile("b", "merge-dest", "order_id"),
				Config: query.ReconcileConfig{ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{Columns: []string{"order_id"}}}},
			}
			mergeable, why := run.Mergeable()
			Expect(mergeable).To(BeFalse())
			Expect(why).To(ContainSubstring("no order is declared"))
		})
	})
})

var _ = Describe("ReconcileConfig", func() {
	config := query.ReconcileConfig{
		Dest:          "orders-ingested",
		SourceFilters: map[string]string{"region": "eu"},
		DestFilters:   map[string]string{"region": "us"},
		ReconcileSpec: query.ReconcileSpec{
			Range:      &query.KeyRange{From: "ord100", To: "ord200"},
			Key:        query.KeySpec{CEL: `row.id`},
			TimeColumn: "created_at",
		},
	}

	It("promotes the join spec so a stored reconcile is one flat block", func() {
		encoded, err := json.Marshal(config)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(encoded)).To(Equal(
			`{"dest":"orders-ingested","sourceFilters":{"region":"eu"},"destFilters":{"region":"us"},"range":{"from":"ord100","to":"ord200"},"key":{"cel":"row.id"},"timeColumn":"created_at"}`))
	})

	It("round-trips through a profile document", func() {
		document, err := yaml.Marshal(query.Profile{Name: "orders-emitted", Reconcile: &config})
		Expect(err).ToNot(HaveOccurred())

		var decoded query.Profile
		Expect(yaml.Unmarshal(document, &decoded)).To(Succeed())
		Expect(decoded.Reconcile).To(Equal(&config))
	})

	It("merges each side's filters without crossing them", func() {
		base := query.ReconcileConfig{
			SourceFilters: map[string]string{"region": "eu", "tier": "gold"},
			DestFilters:   map[string]string{"region": "us", "tenant": "acme"},
		}
		override := query.ReconcileConfig{
			SourceFilters: map[string]string{"region": "za"},
			DestFilters:   map[string]string{"tenant": "tenant-x"},
		}

		merged := query.MergeReconcileConfig(&base, &override)

		Expect(merged.SourceFilters).To(Equal(map[string]string{"region": "za", "tier": "gold"}))
		Expect(merged.DestFilters).To(Equal(map[string]string{"region": "us", "tenant": "tenant-x"}))
	})
})
