package context

import (
	"context"
	"strings"
	"time"

	"github.com/flanksource/commons-db/models"
	commons "github.com/flanksource/commons/context"
	"github.com/google/uuid"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Connection Tests", func() {
	ginkgo.Describe("cache identity", func() {
		ginkgo.It("changes with the stored connection version and resolver scope", func() {
			updatedAt := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
			connection := &models.Connection{ID: uuid.New(), Name: "analytics", UpdatedAt: updatedAt}
			resolverFactory := func() ConnectionResolver {
				return func(string) (*models.Connection, error) { return connection, nil }
			}
			first := NewContext(context.Background()).WithConnectionResolver(resolverFactory())

			original, err := first.ConnectionCacheIdentity("connection://analytics")
			Expect(err).NotTo(HaveOccurred())
			connection.UpdatedAt = updatedAt.Add(time.Hour)
			changed, err := first.ConnectionCacheIdentity("connection://analytics")
			Expect(err).NotTo(HaveOccurred())
			Expect(changed).NotTo(Equal(original))

			second := NewContext(context.Background()).WithConnectionResolver(resolverFactory())
			otherScope, err := second.ConnectionCacheIdentity("connection://analytics")
			Expect(err).NotTo(HaveOccurred())
			Expect(otherScope).NotTo(Equal(changed))

			wrapped := first.Wrap(context.Background())
			wrappedIdentity, err := wrapped.ConnectionCacheIdentity("connection://analytics")
			Expect(err).NotTo(HaveOccurred())
			Expect(wrappedIdentity).To(Equal(changed))
		})

		ginkgo.It("hashes inline credentials instead of retaining them in the key", func() {
			identity, err := NewContext(context.Background()).ConnectionCacheIdentity(
				"postgres://user:sensitive-password@example.invalid/app",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(identity).To(HavePrefix("unscoped:namespace::inline:"))
			Expect(strings.Contains(identity, "sensitive-password")).To(BeFalse())
		})
	})

	ginkgo.It("resolves a virtual connection before consulting the database", func() {
		virtual := &models.Connection{Name: "snapshot", Namespace: "reconciliations", Type: models.ConnectionTypeSQLite}
		ctx := NewContext(context.Background()).WithConnectionResolver(func(reference string) (*models.Connection, error) {
			if reference == "connection://reconciliations/snapshot" {
				return virtual, nil
			}
			return nil, nil
		})
		resolved, err := FindConnectionByURL(ctx, "connection://reconciliations/snapshot")
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(virtual))
	})

	ginkgo.Describe("GetConnectionNameType", func() {
		testCases := []struct {
			name       string
			connection string
			Expect     struct {
				name      string
				namespace string
				found     bool
			}
		}{
			{
				name:       "valid connection string",
				connection: "connection://default/mission_control",
				Expect: struct {
					name      string
					namespace string
					found     bool
				}{
					name:      "mission_control",
					namespace: "default",
					found:     true,
				},
			},
			{
				name:       "empty namespace",
				connection: "connection://  /mission_control",
				Expect: struct {
					name      string
					namespace string
					found     bool
				}{
					name:      "mission_control",
					namespace: "",
					found:     true,
				},
			},
			{
				name:       "invalid connection string",
				connection: "invalid-connection-string",
				Expect: struct {
					name      string
					namespace string
					found     bool
				}{
					name:      "",
					namespace: "",
					found:     false,
				},
			},
			{
				name:       "empty connection string",
				connection: "",
				Expect: struct {
					name      string
					namespace string
					found     bool
				}{
					name:      "",
					namespace: "",
					found:     false,
				},
			},
			{
				name:       "namespace only",
				connection: "connection://default/",
				Expect: struct {
					name      string
					namespace string
					found     bool
				}{
					name:      "",
					namespace: "default",
					found:     false,
				},
			},
		}

		for _, tc := range testCases {
			tc := tc // capture range variable
			ginkgo.Context(tc.name, func() {
				ginkgo.It("should return the correct name, namespace, and found status", func() {
					name, namespace, found := extractConnectionNameType(tc.connection)
					Expect(name).To(Equal(tc.Expect.name))
					Expect(namespace).To(Equal(tc.Expect.namespace))
					Expect(found).To(Equal(tc.Expect.found))
				})
			})
		}
	})

	ginkgo.Describe("HydrateConnection", func() {
		dummyContext := Context{
			Context: commons.NewContext(context.Background()),
		}

		testCases := []struct {
			name       string
			connection models.Connection
			expect     string
		}{
			{
				name: "properties templating",
				connection: models.Connection{
					URL:      "postgres://$(username):$(password)@$(properties.host):$(properties.port)/$(properties.database)",
					Username: "the-username",
					Password: "the-password",
					Properties: map[string]string{
						"host":     "localhost",
						"database": "mission_control",
						"port":     "5443",
					},
				},
				expect: "postgres://the-username:the-password@localhost:5443/mission_control",
			},
			{
				name: "space and newline trimming",
				connection: models.Connection{
					URL: `

                        postgres://$(username):$(password)@$(properties.host):$(properties.port)/$(properties.database)

                    `,
					Username: "  the-username",
					Password: "the-password  ",
					Properties: map[string]string{
						"host":     "localhost",
						"database": "mission_control",
						"port":     "5443",
					},
				},
				expect: "postgres://the-username:the-password@localhost:5443/mission_control",
			},
		}

		for _, tc := range testCases {
			tc := tc // capture range variable
			ginkgo.Context(tc.name, func() {
				ginkgo.It("should return the correct hydrated URL", func() {
					resp, err := HydrateConnection(dummyContext, &tc.connection)
					Expect(err).ToNot(HaveOccurred())
					Expect(resp.URL).To(Equal(tc.expect))
				})
			})
		}
	})
})
