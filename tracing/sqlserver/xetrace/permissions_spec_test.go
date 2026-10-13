package xetrace

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func yes() *int {
	v := 1
	return &v
}

var _ = Describe("XEvent permission mapping", func() {
	It("requires the compatible parent and state permissions before SQL Server 2022", func() {
		report, err := buildPermissionReport(permissionProbeRow{
			Login:               "analytics",
			ProductMajorVersion: 15,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(report.Granted).To(BeFalse())
		Expect(report.MissingPermissions).To(Equal([]string{
			"ALTER ANY EVENT SESSION",
			"VIEW SERVER STATE",
		}))
		Expect(report.GrantStatements).To(Equal([]string{
			"GRANT ALTER ANY EVENT SESSION TO [analytics];",
			"GRANT VIEW SERVER STATE TO [analytics];",
		}))
	})

	It("requires granular lifecycle and performance-state permissions on SQL Server 2022", func() {
		report, err := buildPermissionReport(permissionProbeRow{
			Login:               "analytics",
			ProductMajorVersion: 16,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(report.MissingPermissions).To(Equal([]string{
			"CREATE ANY EVENT SESSION",
			"ALTER ANY EVENT SESSION ENABLE",
			"DROP ANY EVENT SESSION",
			"VIEW SERVER PERFORMANCE STATE",
		}))
	})

	It("accepts the parent event-session permission with the SQL Server 2022 view grant", func() {
		report, err := buildPermissionReport(permissionProbeRow{
			Login:                      "trace_user",
			ProductMajorVersion:        16,
			AlterAnyEventSession:       yes(),
			ViewServerPerformanceState: yes(),
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(report.Granted).To(BeTrue())
		Expect(report.MissingPermissions).To(BeEmpty())
		Expect(report.GrantStatements).To(BeEmpty())
	})

	It("accepts the complete SQL Server 2022 least-privilege set", func() {
		report, err := buildPermissionReport(permissionProbeRow{
			Login:                      "trace_user",
			ProductMajorVersion:        16,
			CreateAnyEventSession:      yes(),
			AlterAnyEventSessionEnable: yes(),
			DropAnyEventSession:        yes(),
			ViewServerPerformanceState: yes(),
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(report.Granted).To(BeTrue())
	})

	It("accepts sysadmin and rejects an unknown product version", func() {
		report, err := buildPermissionReport(permissionProbeRow{
			Login:               "sa",
			ProductMajorVersion: 16,
			IsSysadmin:          yes(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(report.Granted).To(BeTrue())

		_, err = buildPermissionReport(permissionProbeRow{Login: "analytics"})
		Expect(err).To(MatchError("detect SQL Server product major version: received 0"))
	})

	It("escapes the login in generated grant statements", func() {
		report, err := buildPermissionReport(permissionProbeRow{
			Login:               "we]rd",
			ProductMajorVersion: 15,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Join(report.GrantStatements, "\n")).To(ContainSubstring("[we]]rd]"))
	})

	It("renders an actionable permission error", func() {
		err := (&PermissionError{Report: PermissionReport{
			Login:              "analytics",
			MissingPermissions: []string{"ALTER ANY EVENT SESSION"},
			GrantStatements:    []string{"GRANT ALTER ANY EVENT SESSION TO [analytics];"},
		}}).Error()

		Expect(err).To(ContainSubstring("login [analytics]"))
		Expect(err).To(ContainSubstring("missing ALTER ANY EVENT SESSION"))
		Expect(err).To(ContainSubstring("GRANT ALTER ANY EVENT SESSION TO [analytics];"))
	})
})
