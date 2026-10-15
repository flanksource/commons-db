package schema_test

import (
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("account connection schemas", func() {
	DescribeTable("exposes the endpoint and credential needed to test a service",
		func(connectionType string, requiredURL bool) {
			component := schema.ConnectionComponents()[connectionType]
			fields := component["properties"].(schema.Schema)
			Expect(fields).To(HaveKey("url"))
			Expect(fields).To(HaveKey("password"))
			if requiredURL {
				Expect(component["required"]).To(ContainElement("url"))
			}
		},
		Entry("GitHub", models.ConnectionTypeGithub, false),
		Entry("GitLab", models.ConnectionTypeGitlab, true),
		Entry("Azure DevOps", models.ConnectionTypeAzureDevops, true),
	)

	It("scopes S3 to an AWS service rather than a bucket", func() {
		component := schema.ConnectionComponents()[models.ConnectionTypeS3]
		fields := component["properties"].(schema.Schema)
		Expect(fields).To(HaveKey("username"))
		Expect(fields).To(HaveKey("password"))
		properties := fields["properties"].(schema.Schema)["properties"].(schema.Schema)
		Expect(properties).To(HaveKey("region"))
		Expect(properties).NotTo(HaveKey("bucket"))
	})
})
