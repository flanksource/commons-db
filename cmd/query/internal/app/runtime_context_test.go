package app

import (
	"context"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/glebarez/sqlite"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

type runtimeContextKey struct{}

var _ = Describe("runtime context lifecycle", func() {
	It("keeps the connected database when serve replaces the base context", func() {
		gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		Expect(err).NotTo(HaveOccurred())
		files, err := profiles.NewFileStore(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		runtime, err := NewRuntime(
			dbcontext.NewContext(context.Background()).WithDB(gdb, nil),
			files,
			DatabaseOptions{},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(runtime.SetDatabase(gdb)).To(Succeed())

		base := context.WithValue(context.Background(), runtimeContextKey{}, "serve")
		Expect(runtime.SetContext(dbcontext.NewContext(base))).To(Succeed())

		Expect(runtime.Context().Value(runtimeContextKey{})).To(Equal("serve"))
		Expect(runtime.Context().DB()).NotTo(BeNil())
	})
})
