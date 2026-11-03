package schema_test

import (
	"encoding/json"
	"github.com/flanksource/commons-db/query/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenSearch paging schemas", func() {
	It("publishes the connection threshold and inheritable profile overrides", func() {
		connection, err := json.Marshal(schema.Connection(schema.ConnectionOptions{}))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(connection)).To(ContainSubstring(`"paging_threshold"`))
		Expect(string(connection)).To(ContainSubstring(`"default":"10000"`))
		profile, err := json.Marshal(schema.Profile())
		Expect(err).NotTo(HaveOccurred())
		Expect(string(profile)).To(ContainSubstring(`"pagingThreshold"`))
		Expect(string(profile)).To(ContainSubstring(`"pagingMode"`))
	})
})
