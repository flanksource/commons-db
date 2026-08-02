package dbtest

import (
	"testing"

	"github.com/onsi/ginkgo/v2"
)

// ForT resolves a database for a standard library / testify test and registers
// cleanup on t.
func ForT(t *testing.T, opts Options) *DB {
	t.Helper()

	db, cleanup, err := open(t.Context(), opts)
	if err != nil {
		t.Fatalf("dbtest: %v", err)
	}
	db.fail = func(err error) { t.Fatalf("dbtest: %v", err) }

	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("dbtest cleanup: %v", err)
		}
	})
	return db
}

// ForGinkgo resolves a database for a Ginkgo spec and registers cleanup with
// DeferCleanup.
//
// Call it from BeforeAll or BeforeEach — DeferCleanup binds the teardown to
// whichever node is running, so a BeforeAll resolution is torn down after the
// containing Ordered container rather than after each spec.
func ForGinkgo(opts Options) *DB {
	ginkgo.GinkgoHelper()

	db, cleanup, err := open(ginkgo.GinkgoT().Context(), opts)
	if err != nil {
		ginkgo.Fail("dbtest: " + err.Error())
	}
	db.fail = func(err error) { ginkgo.Fail("dbtest: " + err.Error()) }

	ginkgo.DeferCleanup(func() {
		if err := cleanup(); err != nil {
			ginkgo.GinkgoWriter.Printf("dbtest cleanup: %v\n", err)
		}
	})
	return db
}
