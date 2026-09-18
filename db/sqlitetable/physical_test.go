package sqlitetable_test

import (
	"context"
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/db/sqlitetable"
)

var _ = Describe("sqlitetable.PhysicalNames", func() {
	var bare func(string) (bool, error)

	BeforeEach(func() {
		database, err := sql.Open("sqlite", ":memory:")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		bare = sqlitetable.Bare(context.Background(), database)
	})

	DescribeTable("derives a safe physical name for every declared column",
		func(declared, reserved, expected []string) {
			Expect(sqlitetable.PhysicalNames(declared, reserved, bare)).To(Equal(expected))
		},
		Entry("a safe name is kept", []string{"cycleProcessDetailGuid"}, nil, []string{"cycleProcessDetailGuid"}),
		Entry("a space becomes an underscore", []string{"Pod Name"}, nil, []string{"Pod_Name"}),
		Entry("a dot becomes an underscore", []string{"a.b"}, nil, []string{"a_b"}),
		Entry("names equal ignoring case are numbered", []string{"PolicyGuid", "policyGuid"}, nil, []string{"PolicyGuid", "policyGuid_2"}),
		Entry("a name SQLite rejects bare gets a trailing underscore", []string{"transaction"}, nil, []string{"transaction_"}),
		Entry("a keyword SQLite accepts bare is kept", []string{"action"}, nil, []string{"action"}),
		Entry("a leading digit is prefixed", []string{"1st"}, nil, []string{"c_1st"}),
		Entry("a name with no safe character is prefixed", []string{"..."}, nil, []string{"c_"}),
		Entry("a declared name clashing with a reserved one is numbered", []string{"stream_id"}, []string{"stream_id", "seq"}, []string{"stream_id_2"}),
	)

	DescribeTable("asks SQLite's parser which names stand bare",
		func(name string, expected bool) {
			Expect(bare(name)).To(Equal(expected))
		},
		Entry("transaction", "transaction", false),
		Entry("group", "group", false),
		Entry("action", "action", true),
		Entry("event", "event", true),
		Entry("user", "user", true),
		Entry("type", "type", true),
		Entry("trace", "trace", true),
	)

	It("refuses to check a name that is not made of safe characters", func() {
		_, err := bare("x; DROP TABLE y")
		Expect(err).To(MatchError(ContainSubstring("not a safe identifier")))
	})

	It("reports a connection failure rather than calling the name unsafe", func() {
		database, err := sql.Open("sqlite", ":memory:")
		Expect(err).ToNot(HaveOccurred())
		Expect(database.Close()).To(Succeed())
		_, err = sqlitetable.Bare(context.Background(), database)("name")
		Expect(err).To(MatchError(ContainSubstring("database is closed")))
	})
})
