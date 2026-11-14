package connections

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

var _ = Describe("connection API named secrets", func() {
	const signingKey = "jwt-signing-key-canary"
	reference := types.EnvVar{ValueFrom: &types.EnvVarSource{SecretKeyRef: &types.SecretKeySelector{
		LocalObjectReference: types.LocalObjectReference{Name: "tomcat"}, Key: "manager",
	}}}
	stored := func() *models.Connection {
		return &models.Connection{Name: "tomcat", Type: models.ConnectionTypeHTTP, Secrets: types.EnvVarMap{
			"jwt": {ValueStatic: signingKey}, "manager": reference,
		}}
	}

	It("blanks literal secret values in responses but keeps their names and references", func() {
		connection := stored()
		redactConnection(connection)
		Expect(connection.Secrets).To(Equal(types.EnvVarMap{"jwt": {}, "manager": reference}))
	})

	It("keeps a stored secret an edit leaves blank, takes a changed one and drops an omitted one", func() {
		existing := stored()
		applyConnectionUpdate(existing, &models.Connection{Name: "tomcat", Type: models.ConnectionTypeHTTP, Secrets: types.EnvVarMap{
			"jwt": {}, "deploy": {ValueStatic: "deploy-token"},
		}})
		Expect(existing.Secrets).To(Equal(types.EnvVarMap{"jwt": {ValueStatic: signingKey}, "deploy": {ValueStatic: "deploy-token"}}))
	})

	It("masks resolved secret values and redacts them from errors", func() {
		Expect(maskedConnection(stored()).Secrets).To(Equal(map[string]string{"jwt": MaskValue(signingKey), "manager": MaskValue(reference.String())}))
		Expect(sanitizeConnectionError(errors.New("login failed with "+signingKey), stored())).NotTo(ContainSubstring(signingKey))
	})
})
