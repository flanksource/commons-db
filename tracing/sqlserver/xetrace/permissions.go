package xetrace

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// PermissionReport describes whether the connected SQL Server login can run
// the complete server-scoped XEvent lifecycle used by this package.
type PermissionReport struct {
	Login               string   `json:"login"`
	ProductMajorVersion int      `json:"productMajorVersion"`
	Granted             bool     `json:"granted"`
	MissingPermissions  []string `json:"missingPermissions,omitempty"`
	GrantStatements     []string `json:"grantStatements,omitempty"`
}

// PermissionError reports an actionable XEvent permission failure.
type PermissionError struct {
	Report PermissionReport
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf(
		"SQL XEvent tracing is unavailable for login %s: missing %s; required grants: %s",
		quoteIdent(e.Report.Login),
		strings.Join(e.Report.MissingPermissions, ", "),
		strings.Join(e.Report.GrantStatements, " "),
	)
}

type permissionProbeRow struct {
	Login                      string
	ProductMajorVersion        int
	IsSysadmin                 *int
	AlterAnyEventSession       *int
	CreateAnyEventSession      *int
	AlterAnyEventSessionEnable *int
	DropAnyEventSession        *int
	ViewServerState            *int
	ViewServerPerformanceState *int
}

func buildPermissionReport(row permissionProbeRow) (PermissionReport, error) {
	if row.ProductMajorVersion <= 0 {
		return PermissionReport{}, fmt.Errorf(
			"detect SQL Server product major version: received %d",
			row.ProductMajorVersion,
		)
	}
	report := PermissionReport{
		Login:               row.Login,
		ProductMajorVersion: row.ProductMajorVersion,
	}
	if permissionGranted(row.IsSysadmin) {
		report.Granted = true
		return report, nil
	}

	if row.ProductMajorVersion >= 16 {
		parentGranted := permissionGranted(row.AlterAnyEventSession)
		appendMissingPermission(&report, "CREATE ANY EVENT SESSION", parentGranted || permissionGranted(row.CreateAnyEventSession))
		appendMissingPermission(&report, "ALTER ANY EVENT SESSION ENABLE", parentGranted || permissionGranted(row.AlterAnyEventSessionEnable))
		appendMissingPermission(&report, "DROP ANY EVENT SESSION", parentGranted || permissionGranted(row.DropAnyEventSession))
		appendMissingPermission(&report, "VIEW SERVER PERFORMANCE STATE", permissionGranted(row.ViewServerPerformanceState))
	} else {
		appendMissingPermission(&report, "ALTER ANY EVENT SESSION", permissionGranted(row.AlterAnyEventSession))
		appendMissingPermission(&report, "VIEW SERVER STATE", permissionGranted(row.ViewServerState))
	}

	report.Granted = len(report.MissingPermissions) == 0
	return report, nil
}

// CheckPermissions probes the permissions required by Create, Poll, and Drop.
func CheckPermissions(ctx context.Context, db *sql.Conn) (PermissionReport, error) {
	if db == nil {
		return PermissionReport{}, fmt.Errorf("probe SQL XEvent permissions: database is required")
	}
	var row permissionProbeRow
	if err := db.QueryRowContext(ctx, permissionProbeSQL).Scan(
		&row.Login, &row.ProductMajorVersion, &row.IsSysadmin,
		&row.AlterAnyEventSession, &row.CreateAnyEventSession,
		&row.AlterAnyEventSessionEnable, &row.DropAnyEventSession,
		&row.ViewServerState, &row.ViewServerPerformanceState,
	); err != nil {
		return PermissionReport{}, fmt.Errorf("probe SQL XEvent permissions: %w", err)
	}
	return buildPermissionReport(row)
}

const permissionProbeSQL = `
SELECT
  SUSER_SNAME() AS login,
  CAST(SERVERPROPERTY('ProductMajorVersion') AS int) AS product_major_version,
  IS_SRVROLEMEMBER('sysadmin') AS is_sysadmin,
  HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER ANY EVENT SESSION') AS alter_any_event_session,
  HAS_PERMS_BY_NAME(NULL, NULL, 'CREATE ANY EVENT SESSION') AS create_any_event_session,
  HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER ANY EVENT SESSION ENABLE') AS alter_any_event_session_enable,
  HAS_PERMS_BY_NAME(NULL, NULL, 'DROP ANY EVENT SESSION') AS drop_any_event_session,
  HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') AS view_server_state,
  HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE') AS view_server_performance_state`

func permissionGranted(value *int) bool {
	return value != nil && *value == 1
}

func appendMissingPermission(report *PermissionReport, permission string, granted bool) {
	if granted {
		return
	}
	report.MissingPermissions = append(report.MissingPermissions, permission)
	report.GrantStatements = append(
		report.GrantStatements,
		fmt.Sprintf("GRANT %s TO %s;", permission, quoteIdent(report.Login)),
	)
}
