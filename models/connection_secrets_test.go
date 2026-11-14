package models

import (
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/types"
)

var _ = Describe("connection named secrets", func() {
	const (
		managerPassword = "tomcat-manager-canary"
		signingKey      = "jwt-signing-key-canary"
	)
	hydrated := Connection{Name: "tomcat", Secrets: types.EnvVarMap{
		"manager": {ValueStatic: managerPassword},
		"jwt":     {ValueStatic: signingKey},
	}}

	It("returns each hydrated secret by name", func() {
		Expect(hydrated.Secret("manager")).To(Equal(managerPassword))
		Expect(hydrated.Secret("jwt")).To(Equal(signingKey))
	})

	It("fails for a name the connection does not declare, naming the connection and the secret", func() {
		_, err := hydrated.Secret("missing")
		Expect(err).To(MatchError(And(ContainSubstring(`"tomcat"`), ContainSubstring(`"missing"`))))
	})

	It("fails for a secret that is still a reference", func() {
		unhydrated := Connection{Name: "tomcat", Secrets: types.EnvVarMap{"jwt": {ValueFrom: &types.EnvVarSource{
			SecretKeyRef: &types.SecretKeySelector{LocalObjectReference: types.LocalObjectReference{Name: "tomcat"}, Key: "jwt"},
		}}}}
		_, err := unhydrated.Secret("jwt")
		Expect(err).To(MatchError(ContainSubstring("not hydrated")))
	})

	It("keeps secret values out of diagnostic formatting but round-trips them through JSON", func() {
		Expect(fmt.Sprintf("%+v", hydrated)).NotTo(Or(ContainSubstring(managerPassword), ContainSubstring(signingKey)))
		data, err := json.Marshal(hydrated)
		Expect(err).NotTo(HaveOccurred())
		var restored Connection
		Expect(json.Unmarshal(data, &restored)).To(Succeed())
		Expect(restored.Secrets).To(Equal(hydrated.Secrets))
	})
})
