package metrics

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var errBoom = errors.New("boom")

var _ = Describe("profiles.<name> tables", func() {
	It("loads a referenced profile's rows as a table with the profile's columns", func() {
		var asked []string
		rows, err := Execute(context.Background(), Request{
			SQL: `SELECT pod, sum(restarts) AS restarts FROM profiles.pods WHERE phase = 'Running' GROUP BY pod ORDER BY pod`,
			Profiles: func(name string) (ProfileTable, error) {
				asked = append(asked, name)
				return ProfileTable{Columns: []string{"pod", "phase", "restarts"}, Rows: []map[string]any{
					{"pod": "api", "phase": "Running", "restarts": int64(2)},
					{"pod": "api", "phase": "Running", "restarts": int64(1)},
					{"pod": "web", "phase": "Pending", "restarts": int64(5)},
				}}, nil
			},
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal([]string{"pods"}))
		Expect(rows).To(Equal([]map[string]any{{"pod": "api", "restarts": int64(3)}}))
	})

	It("refuses a profile table when no resolver is supplied", func() {
		_, err := Execute(context.Background(), Request{SQL: `SELECT * FROM profiles.pods`}, nil)
		Expect(err).To(MatchError(ContainSubstring("profiles.pods")))
	})

	It("refuses a profile without declared columns or rows", func() {
		_, err := Execute(context.Background(), Request{SQL: `SELECT * FROM profiles.empty`, Profiles: func(string) (ProfileTable, error) { return ProfileTable{}, nil }}, nil)
		Expect(err).To(MatchError(ContainSubstring("no columns")))
	})

	It("still rejects other database-qualified names", func() {
		_, err := Plan(`SELECT * FROM other.cpu`)
		Expect(err).To(MatchError(ContainSubstring("database qualified")))
	})
})
