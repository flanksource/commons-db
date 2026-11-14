package crd_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/connection/crd"
	"github.com/flanksource/commons-db/types"
)

var _ = Describe("a connection's named secrets", func() {
	It("carries spec.secrets onto the model and back as valueFrom references", func() {
		resource := decode("apiVersion: mission-control.flanksource.com/v1\nkind: Connection\nmetadata: {name: tomcat, namespace: deployers}\nspec:\n" +
			"  http:\n    url: https://tomcat.example.test/manager\n" +
			"  secrets:\n    manager: {valueFrom: {secretKeyRef: {name: tomcat, key: manager}}}\n    jwt: {valueFrom: {onePassword: op://ops/tomcat/jwt}}\n")
		model, err := crd.ToModel(resource)
		Expect(err).NotTo(HaveOccurred())
		onePassword := "op://ops/tomcat/jwt"
		Expect(model.Secrets).To(Equal(types.EnvVarMap{
			"manager": {ValueFrom: &types.EnvVarSource{SecretKeyRef: &types.SecretKeySelector{LocalObjectReference: types.LocalObjectReference{Name: "tomcat"}, Key: "manager"}}},
			"jwt":     {ValueFrom: &types.EnvVarSource{OnePassword: &onePassword}},
		}))

		restored, err := crd.FromModel(model)
		Expect(err).NotTo(HaveOccurred())
		Expect(restored.Spec.Secrets).To(Equal(resource.Spec.Secrets))
	})
})
