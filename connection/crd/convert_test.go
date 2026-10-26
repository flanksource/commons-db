package crd_test

import (
	"testing"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"

	"github.com/flanksource/commons-db/connection/crd"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

func TestCRD(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "connection/crd") }

func decode(document string) *crd.Connection {
	var connection crd.Connection
	Expect(yaml.UnmarshalStrict([]byte(document), &connection)).To(Succeed())
	return &connection
}

var _ = Describe("converting a typed connection", func() {
	DescribeTable("maps each typed spec onto the connection mission-control persists",
		func(spec string, expected models.Connection) {
			connection, err := crd.ToModel(decode("apiVersion: mission-control.flanksource.com/v1\nkind: Connection\nmetadata: {name: conn, namespace: ns}\nspec:\n" + spec))
			Expect(err).ToNot(HaveOccurred())
			expected.Name, expected.Namespace = "conn", "ns"
			Expect(connection).To(Equal(expected))
		},
		Entry("mssql from its parts, a secret username kept as a reference",
			"  mssql:\n    host: {value: db:1433}\n    database: {value: orders}\n    username: {valueFrom: {secretKeyRef: {name: orders, key: user}}}\n    password: {valueFrom: {onePassword: op://ops/orders/password}}\n    trustServerCertificate: true\n",
			models.Connection{
				Type:     models.ConnectionTypeSQLServer,
				URL:      "sqlserver://$(username):$(password)@$(properties.host)?database=$(properties.database)&TrustServerCertificate=$(properties.trust_server_certificate)",
				Username: "secret://orders/user", Password: "op://ops/orders/password",
				Properties: types.JSONStringMap{"host": "db:1433", "database": "orders", "trust_server_certificate": "true"},
			}),
		Entry("postgres from its url, with TLS off",
			"  postgres:\n    url: {value: 'postgres://db/app'}\n    insecureTLS: true\n",
			models.Connection{
				Type: models.ConnectionTypePostgres, URL: "postgres://db/app?sslmode=disable",
				Properties: types.JSONStringMap{"host": "", "database": "", "insecure_tls": "true"},
			}),
		Entry("loki",
			"  loki:\n    url: http://loki:3100\n    username: {value: admin}\n",
			models.Connection{Type: models.ConnectionTypeLoki, URL: "http://loki:3100", Username: "admin"}),
		Entry("aws, its region and role as properties",
			"  aws:\n    region: eu-west-1\n    accessKey: {value: AKIA}\n    secretKey: {valueFrom: {secretKeyRef: {name: aws, key: secret}}}\n    assumeRole: arn:aws:iam::1:role/r\n",
			models.Connection{
				Type: models.ConnectionTypeAWS, Username: "AKIA", Password: "secret://aws/secret",
				Properties: types.JSONStringMap{"region": "eu-west-1", "profile": "", "sessionToken": "", "assumeRole": "arn:aws:iam::1:role/r"},
			}),
		Entry("opensearch, its nodes and index as properties",
			"  opensearch:\n    urls: [https://os-1:9200, https://os-2:9200]\n    index: logs-*\n    username: {value: reader}\n",
			models.Connection{
				Type: models.ConnectionTypeOpenSearch, URL: "https://os-1:9200", Username: "reader",
				Properties: types.JSONStringMap{"urls": "https://os-1:9200,https://os-2:9200", "index": "logs-*", "insecure_tls": "false"},
			}),
		Entry("kubernetes, its kubeconfig the certificate",
			"  kubernetes:\n    certificate: {valueFrom: {secretKeyRef: {name: kube, key: config}}}\n",
			models.Connection{Type: models.ConnectionTypeKubernetes, Certificate: "secret://kube/config"}),
		Entry("smtp, defaulted as mission-control defaults it",
			"  smtp:\n    host: mail\n    fromName: alerts\n",
			models.Connection{
				Type: models.ConnectionTypeEmail,
				URL:  "smtp://$(username):$(password)@mail:25/?UseStartTLS=false&Encryption=Auto&Auth=plain&from=no-reply@example.com&to=no-reply@example.com",
				Properties: types.JSONStringMap{
					"port": "25", "subject": "", "auth": "plain", "from": "no-reply@example.com", "to": "no-reply@example.com",
					"fromname": "alerts", "headers": "{}",
				},
			}),
	)

	It("adds the spec's own properties beside the typed ones, the typed ones winning", func() {
		connection, err := crd.ToModel(decode("kind: Connection\nmetadata: {name: conn}\nspec:\n  properties: {region: us-east-1, extra: x}\n  aws: {region: eu-west-1}\n"))
		Expect(err).ToNot(HaveOccurred())
		Expect(connection.Properties).To(HaveKeyWithValue("region", "eu-west-1"))
		Expect(connection.Properties).To(HaveKeyWithValue("extra", "x"))
	})

	It("keeps a connection that names its type the deprecated way", func() {
		connection, err := crd.ToModel(decode("kind: Connection\nmetadata: {name: conn}\nspec:\n  type: postgres\n  url: {value: 'postgres://db'}\n"))
		Expect(err).ToNot(HaveOccurred())
		Expect(connection.Type).To(Equal("postgres"))
		Expect(connection.URL).To(Equal("postgres://db"))
	})

	DescribeTable("FromModel is the resource ToModel reads back as the same persisted connection",
		func(connection models.Connection) {
			connection.Name, connection.Namespace = "conn", "ns"
			resource, err := crd.FromModel(connection)
			Expect(err).ToNot(HaveOccurred())
			Expect(resource.APIVersion).To(Equal(crd.APIVersion))
			Expect(resource.Kind).To(Equal(crd.Kind))

			encoded, err := yaml.Marshal(resource)
			Expect(err).ToNot(HaveOccurred())
			Expect(crd.ToModel(decode(string(encoded)))).To(Equal(connection))
		},
		Entry("postgres with a secret password", models.Connection{
			Type: models.ConnectionTypePostgres, URL: "postgres://$(username):$(password)@db/app",
			Username: "app", Password: "secret://db/password",
			Properties: types.JSONStringMap{"sslmode": "disable"},
		}),
		Entry("sqlite, which has no typed variant", models.Connection{
			Type: models.ConnectionTypeSQLite, URL: "file:/data/app.db?mode=ro",
		}),
		Entry("sql server with a configmap username and a 1password password", models.Connection{
			Type: models.ConnectionTypeSQLServer, URL: "sqlserver://$(username):$(password)@db:1433",
			Username: "configmap://orders/user", Password: "op://ops/orders/password",
		}),
		Entry("aws with TLS verification off and a helm secret key", models.Connection{
			Type: models.ConnectionTypeAWS, Username: "AKIA", Password: "helm://aws/credentials.secret",
			InsecureTLS: true, Properties: types.JSONStringMap{"region": "eu-west-1"},
		}),
		Entry("kubernetes with a service-account certificate", models.Connection{
			Type: models.ConnectionTypeKubernetes, Certificate: "serviceaccount://reader",
		}),
		Entry("a persisted id", models.Connection{
			ID: uuid.MustParse("5d41402a-bc4b-4a76-b971-9d911017c592"), Type: models.ConnectionTypeLoki, URL: "http://loki:3100",
		}),
	)

	It("refuses a connection that names no type", func() {
		_, err := crd.FromModel(models.Connection{Name: "conn"})
		Expect(err).To(MatchError(ContainSubstring(`connection "conn" has no type`)))
	})

	It("refuses a credential that is a malformed reference", func() {
		_, err := crd.FromModel(models.Connection{Name: "conn", Type: models.ConnectionTypePostgres, Password: "secret://only-a-name"})
		Expect(err).To(MatchError(ContainSubstring("password")))
	})

	DescribeTable("refuses a resource that is not one connection",
		func(document, expected string) {
			_, err := crd.ToModel(decode(document))
			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("another kind", "kind: Playbook\nspec: {loki: {url: x}}\n", `kind is "Playbook"`),
		Entry("another api version", "apiVersion: v1\nkind: Connection\nspec: {loki: {url: x}}\n", `apiVersion is "v1"`),
		Entry("no kind of connection", "kind: Connection\nspec: {properties: {a: b}}\n", "names no kind of connection"),
		Entry("two kinds of connection", "kind: Connection\nspec: {loki: {url: x}, postgres: {}}\n", "spec sets loki and postgres"),
		Entry("a uid that is not a UUID", "kind: Connection\nmetadata: {uid: nope}\nspec: {loki: {url: x}}\n", "failed to parse uid"),
	)
})
