package connection

import (
	"context"
	"database/sql/driver"
	"regexp"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("backend capabilities", func() {
	probe := func(backend, statement string, columns []string, values ...driver.Value) (BackendCapabilities, error) {
		db, mock, err := sqlmock.New()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = db.Close() })
		mock.ExpectQuery(regexp.QuoteMeta(statement)).WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
		capabilities, err := ProbeBackendCapabilities(context.Background(), db, backend)
		Expect(mock.ExpectationsWereMet()).To(Succeed())
		return capabilities, err
	}

	It("reads PostgreSQL's numeric server version", func() {
		capabilities, err := probe("postgres", postgresCapabilitiesQuery,
			[]string{"database", "version", "version_num"}, "warehouse", "17.4", 170004)
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Database).To(Equal("warehouse"))
		Expect(capabilities.Features[BackendCapabilityArrayFilters]).To(Equal(BackendCapabilityStatus{
			Supported: true, Detail: "native and JSON arrays", MinimumVersion: "9.5",
		}))
	})

	DescribeTable("classifies MySQL array filtering from live version metadata",
		func(version string, supported bool, detail, minimum string) {
			capabilities, err := probe("mysql", mysqlCapabilitiesQuery,
				[]string{"database", "version"}, "warehouse", version)
			Expect(err).ToNot(HaveOccurred())
			Expect(capabilities.Features[BackendCapabilityArrayFilters]).To(Equal(BackendCapabilityStatus{
				Supported: supported, Detail: detail, MinimumVersion: minimum,
			}))
		},
		Entry("the minimum MySQL release", "8.0.4", true, "JSON arrays via JSON_TABLE", "8.0.4"),
		Entry("an older MySQL release", "8.0.3", false, "JSON arrays via JSON_TABLE", "8.0.4"),
		Entry("the minimum MariaDB release", "10.6.0-MariaDB", true, "JSON arrays via JSON_TABLE", "10.6.0"),
		Entry("an older MariaDB release", "10.5.27-MariaDB", false, "JSON arrays via JSON_TABLE", "10.6.0"),
	)

	It("uses SQL Server database compatibility rather than the Azure product version", func() {
		capabilities, err := probe("sqlserver", sqlServerCapabilitiesQuery,
			[]string{"database", "version", "compatibility_level"}, "warehouse", "12.0.2000.8", 160)
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Version).To(Equal("12.0.2000.8"))
		Expect(capabilities.CompatibilityLevel).To(HaveValue(Equal(160)))
		Expect(capabilities.Features[BackendCapabilityArrayFilters]).To(Equal(BackendCapabilityStatus{
			Supported: true, Detail: "JSON arrays via OPENJSON", MinimumCompatibilityLevel: new(130),
		}))
	})

	It("rejects SQL Server databases below compatibility level 130", func() {
		capabilities, err := probe("sql_server", sqlServerCapabilitiesQuery,
			[]string{"database", "version", "compatibility_level"}, "warehouse", "16.0.1000.6", 120)
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Features[BackendCapabilityArrayFilters].Supported).To(BeFalse())
		Expect(capabilities.Require(BackendCapabilityArrayFilters)).To(MatchError(ContainSubstring("compatibility level 130")))
	})

	DescribeTable("probes JSON expansion instead of assuming SQLite build features",
		func(result int, supported bool) {
			capabilities, err := probe("sqlite", sqliteCapabilitiesQuery,
				[]string{"database", "version", "json_arrays"}, "main", "3.50.4", result)
			Expect(err).ToNot(HaveOccurred())
			Expect(capabilities.Features[BackendCapabilityArrayFilters].Supported).To(Equal(supported))
		},
		Entry("JSON functions are present", 1, true),
		Entry("JSON functions return an unexpected result", 0, false),
	)

	It("reports ClickHouse array support as a static server feature", func() {
		capabilities, err := probe("clickhouse", clickHouseCapabilitiesQuery,
			[]string{"database", "version"}, "default", "25.8.3.66")
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Features[BackendCapabilityArrayFilters]).To(Equal(BackendCapabilityStatus{
			Supported: true, Detail: "native Array(String) values",
		}))
	})

	It("fails when required metadata is empty", func() {
		_, err := probe("postgres", postgresCapabilitiesQuery,
			[]string{"database", "version", "version_num"}, "warehouse", "", 170004)
		Expect(err).To(MatchError(ContainSubstring("version is empty")))
	})

	It("fails when a live probe has no selected database", func() {
		_, err := probe("mysql", mysqlCapabilitiesQuery,
			[]string{"database", "version"}, "", "8.0.21")
		Expect(err).To(MatchError(ContainSubstring("database is empty")))
	})

	It("fails when version metadata cannot be interpreted", func() {
		_, err := probe("mysql", mysqlCapabilitiesQuery,
			[]string{"database", "version"}, "warehouse", "not-a-version")
		Expect(err).To(MatchError(ContainSubstring("parse MySQL version")))
	})

	It("rejects capability metadata without a feature status", func() {
		err := (BackendCapabilities{Backend: "postgres", Features: map[BackendCapability]BackendCapabilityStatus{}}).Validate()
		Expect(err).To(MatchError(ContainSubstring("features are missing")))
	})

	It("fingerprints feature metadata deterministically", func() {
		left := BackendCapabilities{
			Backend: "postgres", Version: "17.4", Database: "warehouse",
			Features: map[BackendCapability]BackendCapabilityStatus{
				BackendCapabilityArrayFilters: {Supported: true, MinimumVersion: "9.5"},
			},
		}
		right := left
		right.Features = map[BackendCapability]BackendCapabilityStatus{
			BackendCapabilityArrayFilters: {Supported: true, MinimumVersion: "9.5"},
		}
		leftFingerprint, err := left.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		rightFingerprint, err := right.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		Expect(rightFingerprint).To(Equal(leftFingerprint))

		right.Version = "17.5"
		changed, err := right.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		Expect(changed).ToNot(Equal(leftFingerprint))
	})

	It("fingerprints static provider capabilities without invented server metadata", func() {
		capabilities := BackendCapabilities{
			Backend: "opensearch",
			Features: map[BackendCapability]BackendCapabilityStatus{
				BackendCapabilityArrayFilters: {Supported: true, Detail: "native keyword arrays"},
			},
		}

		fingerprint, err := capabilities.Fingerprint()
		Expect(err).ToNot(HaveOccurred())
		Expect(fingerprint).To(HavePrefix("backend-capabilities-v1:"))
		Expect(capabilities.Require(BackendCapabilityArrayFilters)).To(Succeed())
	})
})
