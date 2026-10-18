package connection_test

import (
	"github.com/flanksource/commons-db/connection"
	dbgit "github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GitConnection.CloneOptions", func() {
	depth := func(n int) *int { return &n }

	DescribeTable("merges the URL query over the connection fields",
		func(conn connection.GitConnection, expected dbgit.CloneOptions) {
			options, err := conn.CloneOptions("/work")
			Expect(err).ToNot(HaveOccurred())
			Expect(options).To(Equal(expected))
		},
		Entry("defaults: remote HEAD at depth 1", connection.GitConnection{URL: "https://example.com/acme/rules.git"},
			dbgit.CloneOptions{Dir: "/work", Depth: 1}),
		Entry("fields", connection.GitConnection{URL: "https://example.com/acme/rules.git", Branch: "dev", Depth: depth(0)},
			dbgit.CloneOptions{Dir: "/work", Ref: "dev", Depth: 0}),
		Entry("?ref and ?depth win", connection.GitConnection{URL: "https://example.com/acme/rules.git?ref=v2&depth=3", Branch: "dev", Depth: depth(10)},
			dbgit.CloneOptions{Dir: "/work", Ref: "v2", Depth: 3}),
	)

	It("rejects a malformed ?depth", func() {
		_, err := connection.GitConnection{URL: "https://example.com/acme/rules.git?depth=x"}.CloneOptions("/work")
		Expect(err).To(MatchError(ContainSubstring("invalid depth")))
	})
})
