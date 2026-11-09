package app

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

var _ = Describe("Kubernetes connection logs profiles", func() {
	cluster := &models.Connection{Name: "k3s", Type: models.ConnectionTypeKubernetes}
	restricted := &models.Connection{
		Name: "payments-cluster", Namespace: "platform", Type: models.ConnectionTypeKubernetes,
		Properties: map[string]string{"namespace": "payments"},
	}
	database := &models.Connection{Name: "orders", Type: models.ConnectionTypePostgres}
	var asked []string
	store := connectionLogProfiles{list: func(types string) ([]*models.Connection, error) {
		asked = append(asked, types)
		return []*models.Connection{cluster, restricted, database}, nil
	}}

	It("generates one read-only logs profile per Kubernetes connection", func() {
		profiles, err := store.List(context.Background())
		Expect(err).ToNot(HaveOccurred())
		Expect(asked).To(ContainElement(models.ConnectionTypeKubernetes))

		names := []string{}
		for _, profile := range profiles {
			names = append(names, profile.Name)
			Expect(profile.Virtual).To(BeTrue())
			Expect(profile.ReadOnly).To(BeTrue())
			Expect(profile.Render).To(Equal(query.RenderLogs))
			Expect(profile.Provider.Type).To(Equal("k8s"))
		}
		Expect(names).To(Equal([]string{"Logs: k3s", "Logs: platform/payments-cluster"}))
	})

	// The namespace restriction is the profile's scope, so the generated
	// Namespace picker disappears exactly when the connection names one.
	It("scopes a restricted connection's profile to its namespace and leaves the rest open", func() {
		open, err := store.Get(context.Background(), "Logs: k3s")
		Expect(err).ToNot(HaveOccurred())
		Expect(open.Provider.Connection).To(Equal("connection://k3s"))
		Expect(open.Query).To(BeEmpty())
		bindings, err := open.RuntimeFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(ContainElement(HaveField("Key", "namespace")))

		pinned, err := store.Get(context.Background(), "Logs: platform/payments-cluster")
		Expect(err).ToNot(HaveOccurred())
		Expect(pinned.Provider.Connection).To(Equal("connection://platform/payments-cluster"))
		Expect(pinned.Query).To(Equal("namespace=payments"))
		bindings, err = pinned.RuntimeFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).ToNot(ContainElement(HaveField("Key", "namespace")))
	})

	It("claims only its own names and refuses writes", func() {
		Expect(store.IsVirtual("Logs: k3s")).To(BeTrue())
		Expect(store.IsVirtual("App logs")).To(BeFalse())
		_, err := store.Get(context.Background(), "Logs: missing")
		Expect(err).To(MatchError(ContainSubstring(`no Kubernetes connection "missing"`)))
		Expect(store.Save(context.Background(), query.Profile{Name: "Logs: k3s"})).To(MatchError(ContainSubstring("read-only")))
	})
})
