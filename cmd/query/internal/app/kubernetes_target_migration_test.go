package app

import (
	"encoding/json"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Kubernetes profile target migration", func() {
	It("moves persisted target options into the query without changing log options", func() {
		handle := dbtest.ForGinkgo(dbtest.Options{
			Name: "query_kubernetes_target_migration", LogName: "query-kubernetes-target-migration-test",
		})
		Expect(handle.Gorm().Exec(`CREATE TABLE profiles (
			id text PRIMARY KEY,
			name text NOT NULL UNIQUE,
			namespace text,
			spec jsonb NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		)`).Error).ToNot(HaveOccurred())
		profile := query.Profile{
			Name: "Kubernetes Pod Logs",
			Provider: query.ProviderConfig{Type: "k8s", Options: map[string]any{
				"kind": "Pod", "namespace": "zimbabwe", "name": "example-57d6979b5d-cdq2b", "limit": "500",
			}},
		}
		spec, err := json.Marshal(profile)
		Expect(err).ToNot(HaveOccurred())
		Expect(handle.Gorm().Exec(
			"INSERT INTO profiles (id, name, spec) VALUES (?, ?, ?::jsonb)", "legacy-profile", profile.Name, string(spec),
		).Error).ToNot(HaveOccurred())
		selected := profile
		selected.Name = "Selected Kubernetes Pod Logs"
		selected.Query = "kind=Pod namespace=zimbabwe name=example-57d6979b5d-cdq2b"
		selected.Provider.Options["namespace"] = "example"
		selected.Provider.Options["name"] = "cycle-0"
		selectedSpec, err := json.Marshal(selected)
		Expect(err).ToNot(HaveOccurred())
		Expect(handle.Gorm().Exec(
			"INSERT INTO profiles (id, name, spec) VALUES (?, ?, ?::jsonb)", "selected-profile", selected.Name, string(selectedSpec),
		).Error).ToNot(HaveOccurred())

		Expect(migrateSchema(GinkgoT().Context(), handle.DSN())).To(Succeed())

		var migratedSpec string
		Expect(handle.Gorm().Raw("SELECT spec FROM profiles WHERE name = ?", profile.Name).Scan(&migratedSpec).Error).ToNot(HaveOccurred())
		var migrated query.Profile
		Expect(json.Unmarshal([]byte(migratedSpec), &migrated)).To(Succeed())
		Expect(migrated.Query).To(Equal("kind=Pod namespace=zimbabwe name=example-57d6979b5d-cdq2b"))
		Expect(migrated.Provider.Options).To(Equal(map[string]any{"limit": "500"}))

		Expect(handle.Gorm().Raw("SELECT spec FROM profiles WHERE name = ?", selected.Name).Scan(&migratedSpec).Error).ToNot(HaveOccurred())
		Expect(json.Unmarshal([]byte(migratedSpec), &migrated)).To(Succeed())
		Expect(migrated.Query).To(Equal(selected.Query))
		Expect(migrated.Provider.Options).To(Equal(map[string]any{"limit": "500"}))
	})
})
