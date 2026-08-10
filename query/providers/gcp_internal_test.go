package providers

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
)

var _ = Describe("gcp connection for a request", func() {
	// A GCP query with no connection runs as whatever identity the environment
	// provides, so the project has to be resolvable from there too.
	BeforeEach(func() {
		// Point the application-default lookup at a path that does not exist, so
		// a developer's own gcloud credentials cannot decide the outcome.
		GinkgoT().Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(GinkgoT().TempDir(), "absent.json"))
		GinkgoT().Setenv("GOOGLE_CLOUD_PROJECT", "")
		GinkgoT().Setenv("GCLOUD_PROJECT", "")
	})

	It("takes the project from the environment when neither the option nor a connection names one", func() {
		GinkgoT().Setenv("GOOGLE_CLOUD_PROJECT", "billing-prod")

		conn, err := gcpConnectionForRequest(dbcontext.New(), "", "")
		Expect(err).ToNot(HaveOccurred())
		Expect(conn.Project).To(Equal("billing-prod"))
	})

	It("prefers the explicit option over the environment", func() {
		GinkgoT().Setenv("GOOGLE_CLOUD_PROJECT", "billing-prod")

		conn, err := gcpConnectionForRequest(dbcontext.New(), "", "analytics-staging")
		Expect(err).ToNot(HaveOccurred())
		Expect(conn.Project).To(Equal("analytics-staging"))
	})

	It("names every way to supply a project when none of them did", func() {
		_, err := gcpConnectionForRequest(dbcontext.New(), "", "")
		Expect(err).To(MatchError(ContainSubstring("project")))
		Expect(err).To(MatchError(ContainSubstring("GOOGLE_CLOUD_PROJECT")))
	})
})
