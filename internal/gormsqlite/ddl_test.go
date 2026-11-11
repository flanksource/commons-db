package sqlite

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSQLiteDialector(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Modernc GORM dialector")
}

var _ = Describe("SQLite DDL", func() {
	It("preserves constraints and defaults while removing a column and renaming a copy", func() {
		original, err := parseDDL("CREATE TABLE notes (`id` integer PRIMARY KEY, `body` text NOT NULL DEFAULT 'acme', `obsolete` text, CONSTRAINT uq_body UNIQUE (body))")
		Expect(err).NotTo(HaveOccurred())
		Expect(original.hasConstraint("uq_body")).To(BeTrue())
		copy := original.clone()
		Expect(copy.removeColumn("obsolete")).To(BeTrue())
		Expect(copy.renameTable("notes_copy", "notes")).To(Succeed())
		Expect(copy.getColumns()).To(Equal([]string{"`id`", "`body`"}))
		Expect(copy.compile()).To(ContainSubstring("CREATE TABLE `notes_copy`"))
		Expect(original.getColumns()).To(HaveLen(3))
		Expect(copy.removeConstraint("uq_body")).To(BeTrue())
		Expect(copy.hasConstraint("uq_body")).To(BeFalse())
		copy.addConstraint("uq_body", "CONSTRAINT uq_body UNIQUE (body)")
		Expect(copy.hasConstraint("uq_body")).To(BeTrue())
	})
	It("rejects unbalanced column expressions", func() {
		_, err := parseDDL("CREATE TABLE broken (body text DEFAULT (lower('acme'))")
		Expect(err).To(MatchError("invalid DDL, unbalanced brackets"))
	})
})

var _ = DescribeTable("SQLite version comparison",
	func(left, right string, expected int) { Expect(compareVersion(left, right)).To(Equal(expected)) },
	Entry("returning supported", "3.35.0", "3.34.9", 1),
	Entry("same version", "3.35.0", "3.35.0", 0),
	Entry("numeric components", "3.9.0", "3.35.0", -1),
)
