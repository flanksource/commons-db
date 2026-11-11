package sqlactivity

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("names a statement's command by its first keyword", func(statement, command string) {
	Expect(commandOf(statement)).To(Equal(command))
},
	Entry("a select", "  select 1", "SELECT"),
	Entry("a call with parentheses", "vacuum(analyze) orders", "VACUUM"),
	Entry("no statement", "   ", ""),
)
