// Specs for recognising a help, version or completion request anywhere in the
// arguments, so building its command tree starts no database.

package commands

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("requestsMetadataOnly",
	func(args []string, metadataOnly bool) {
		Expect(requestsMetadataOnly(args)).To(Equal(metadataOnly))
	},
	Entry("no command", []string{}, true),
	Entry("the root's --help", []string{"--help"}, true),
	Entry("the help command", []string{"help", "serve"}, true),
	Entry("a command's --help", []string{"serve", "--help"}, true),
	Entry("a command's -h", []string{"serve", "-h"}, true),
	Entry("a sub-command's --help after flags", []string{"--config-dir", "/tmp/q", "connection", "list", "--help"}, true),
	Entry("a command", []string{"serve"}, false),
	Entry("a command with flags", []string{"serve", "--port", "8080"}, false),
	Entry("--help as a flag's value", []string{"--config-dir", "--help", "serve"}, false),
	Entry("--help after the end of flags", []string{"trace", "--", "--help"}, false),
)
