package dbtest

import (
	"testing"

	"github.com/onsi/ginkgo/v2"
)

// ForT resolves a database for a standard library / testify test and registers
// a non-blocking, non-fatal teardown on t.
func ForT(t *testing.T, opts Options) *DB {
	t.Helper()

	db, _, err := open(t.Context(), opts)
	if err != nil {
		t.Fatalf("dbtest: %v", err)
	}
	db.fail = func(err error) { t.Fatalf("dbtest: %v", err) }

	t.Cleanup(db.releaseInBackground)
	return db
}

// ForGinkgo resolves a database for a Ginkgo spec and registers a
// non-blocking, non-fatal teardown with DeferCleanup.
//
// Call it from BeforeAll or BeforeEach — DeferCleanup binds the teardown to
// whichever node is running, so a BeforeAll resolution is torn down after the
// containing Ordered container rather than after each spec.
func ForGinkgo(opts Options) *DB {
	ginkgo.GinkgoHelper()

	db, _, err := open(ginkgo.GinkgoT().Context(), opts)
	if err != nil {
		ginkgo.Fail("dbtest: " + err.Error())
	}
	db.fail = func(err error) { ginkgo.Fail("dbtest: " + err.Error()) }

	ginkgo.DeferCleanup(db.releaseInBackground)
	return db
}
