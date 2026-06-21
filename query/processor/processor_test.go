package processor_test

import (
	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/processor"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// statusByKey indexes recon output rows by a key column to their status.
func statusByKey(rows []query.Row, key string) map[string]string {
	out := map[string]string{}
	for _, r := range rows {
		out[toStr(r[key])] = toStr(r[processor.ReconStatusColumn])
	}
	return out
}

func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return string(rune('0' + x)) // sufficient for single-digit test ids
	default:
		return ""
	}
}

var _ = Describe("Merge (in-memory sqlite)", func() {
	It("joins two result sets and aggregates", func() {
		users := processor.ResultSet{Name: "users", Rows: []query.Row{
			{"id": 1, "name": "alice"},
			{"id": 2, "name": "bob"},
		}}
		orders := processor.ResultSet{Name: "orders", Rows: []query.Row{
			{"user_id": 1, "total": 100.0},
			{"user_id": 1, "total": 50.0},
			{"user_id": 2, "total": 30.0},
		}}

		merged, err := processor.Merge(context.New(),
			`select u.name as name, sum(o.total) as total
			   from users u join orders o on u.id = o.user_id
			  group by u.name order by u.name`,
			users, orders)
		Expect(err).ToNot(HaveOccurred())
		Expect(merged).To(HaveLen(2))

		totals := map[string]float64{}
		for _, r := range merged {
			totals[r["name"].(string)] = r["total"].(float64)
		}
		Expect(totals).To(HaveKeyWithValue("alice", 150.0))
		Expect(totals).To(HaveKeyWithValue("bob", 30.0))
	})
})

var _ = Describe("Recon", func() {
	baseline := []query.Row{
		{"id": 1, "status": "A"},
		{"id": 2, "status": "B"},
		{"id": 3, "status": "C"},
	}
	target := []query.Row{
		{"id": 1, "status": "A"},
		{"id": 2, "status": "X"},
		{"id": 4, "status": "D"},
	}

	It("classifies rows as unchanged/changed/added/removed", func() {
		rows, err := processor.Recon(baseline, target, processor.ReconOptions{
			Key:     []string{"id"},
			Compare: []string{"status"},
		})
		Expect(err).ToNot(HaveOccurred())

		statuses := statusByKey(rows, "id")
		Expect(statuses).To(HaveKeyWithValue("1", processor.ReconUnchanged))
		Expect(statuses).To(HaveKeyWithValue("2", processor.ReconChanged))
		Expect(statuses).To(HaveKeyWithValue("4", processor.ReconAdded))
		Expect(statuses).To(HaveKeyWithValue("3", processor.ReconRemoved))
	})

	It("records the from/to diff for changed rows", func() {
		rows, err := processor.Recon(baseline, target, processor.ReconOptions{Key: []string{"id"}})
		Expect(err).ToNot(HaveOccurred())

		var changed query.Row
		for _, r := range rows {
			if toStr(r["id"]) == "2" {
				changed = r
			}
		}
		Expect(changed).ToNot(BeNil())
		changes := changed[processor.ReconChangesColumn].(map[string]any)
		Expect(changes).To(HaveKey("status"))
		Expect(changes["status"]).To(HaveKeyWithValue("from", "B"))
		Expect(changes["status"]).To(HaveKeyWithValue("to", "X"))
	})

	It("requires at least one key column", func() {
		_, err := processor.Recon(baseline, target, processor.ReconOptions{})
		Expect(err).To(MatchError(ContainSubstring("key column")))
	})
})

// fixedProvider returns a fixed set of rows so the processor pipeline can be
// exercised through query.Execute.
type fixedProvider struct {
	rows []query.Row
}

func (fixedProvider) Type() string { return "recon-fixture" }
func (f fixedProvider) Execute(_ context.Context, _ query.ProviderRequest) ([]query.Row, error) {
	return f.rows, nil
}

var _ = Describe("recon processor via Execute", func() {
	It("reconciles the query result against an inline baseline", func() {
		query.RegisterProvider(fixedProvider{rows: []query.Row{
			{"id": 1, "status": "A"},
			{"id": 2, "status": "X"},
		}})

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "recon-pipeline",
			Provider: query.ProviderConfig{Type: "recon-fixture"},
			Processors: []query.ProcessorSpec{{
				Type: "sqlite.recon",
				Config: map[string]any{
					"key": []string{"id"},
					"baseline": []map[string]any{
						{"id": 1, "status": "A"},
						{"id": 2, "status": "B"},
					},
				},
			}},
		})
		Expect(err).ToNot(HaveOccurred())

		statuses := statusByKey(result.Rows, "id")
		Expect(statuses).To(HaveKeyWithValue("1", processor.ReconUnchanged))
		Expect(statuses).To(HaveKeyWithValue("2", processor.ReconChanged))
	})
})
