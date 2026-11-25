package drivers

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDrivers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Drivers Suite")
}

var _ = Describe("ParseURL", func() {
	It("fails naming drivers/cloudsql when a Cloud SQL DSN has no dialer registered", func() {
		RegisterDialer(nil)
		_, err := ParseURL("db=d user=u cloudsql-instance-connection-name=p:r:i")
		Expect(err).To(MatchError(ContainSubstring("drivers/cloudsql")))
	})

	It("hands the parsed Cloud SQL options to the registered dialer", func() {
		var got CloudSQLOptions
		RegisterDialer(func(_ context.Context, o CloudSQLOptions) (*pgx.ConnConfig, error) {
			got = o
			return &pgx.ConnConfig{}, nil
		})
		DeferCleanup(func() { RegisterDialer(nil) })
		_, err := ParseURL("db=d user=u cloudsql-instance-connection-name=p:r:i use-private-ip=true")
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal(CloudSQLOptions{User: "u", Database: "d", InstanceConnectionName: "p:r:i", UsePrivateIP: true}))
	})
})

var _ = Describe("ParseParams", parseParamsSpecs)

func parseParamsSpecs() {
	tests := []struct {
		input  string
		output map[string]string
	}{
		{
			input:  "db=yourdb user=youruser cloudsql-instance-connection-name=yourconnectionname private_ip=true",
			output: map[string]string{"db": "yourdb", "user": "youruser", "cloudsql-instance-connection-name": "yourconnectionname", "private_ip": "true"},
		},
		{
			input:  "key1=value1 key2=value2 key3=value3",
			output: map[string]string{"key1": "value1", "key2": "value2", "key3": "value3"},
		},
		// Add more test cases as needed
	}

	for _, test := range tests {
		test := test // capture range variable
		It("should parse "+test.input, func() {
			result := parseParams(test.input)
			Expect(result).To(Equal(test.output))
		})
	}
}
