package dbtest

import (
	"errors"
	"testing"

	"github.com/onsi/ginkgo/v2"
)

// ForT resolves a database for a standard library / testify test, skipping when
// none is configured and registering cleanup on t.
func ForT(t *testing.T, opts Options) *DB {
	t.Helper()
	if opts.DataDir == "" {
		opts.DataDir = t.TempDir()
	}

	db, cleanup, err := Open(opts)
	if errors.Is(err, ErrSkip) {
		t.Skip(err.Error())
	}
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

// ForGinkgo resolves a database for a Ginkgo spec, skipping when none is
// configured and registering cleanup with DeferCleanup.
//
// Call it from BeforeAll or BeforeEach — DeferCleanup binds the teardown to
// whichever node is running, so a BeforeAll resolution is torn down after the
// containing Ordered container rather than after each spec.
func ForGinkgo(opts Options) *DB {
	ginkgo.GinkgoHelper()
	if opts.DataDir == "" {
		opts.DataDir = ginkgo.GinkgoT().TempDir()
	}

	db, cleanup, err := Open(opts)
	if errors.Is(err, ErrSkip) {
		ginkgo.Skip(err.Error())
	}
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
