package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/lib/pq"
)

type managedSecurityState struct {
	Permissions []permissionSpec `json:"permissions,omitempty"`
	Memberships []membershipSpec `json:"memberships,omitempty"`
}

type membershipSpec struct {
	Member string `json:"member"`
	Parent string `json:"parent"`
}

type permissionTarget struct {
	Kind, Schema, Object, Column string
}

var allowedPrivileges = map[string]map[string]bool{
	"schema":   {"USAGE": true, "CREATE": true},
	"table":    {"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": true, "REFERENCES": true, "TRIGGER": true, "MAINTAIN": true},
	"column":   {"SELECT": true, "INSERT": true, "UPDATE": true, "REFERENCES": true},
	"sequence": {"USAGE": true, "SELECT": true, "UPDATE": true},
}

func validatePermission(p permissionSpec) error {
	if p.Grantee == "" {
		return fmt.Errorf("permission grantee is empty")
	}
	target, err := parsePermissionTarget(p.Target)
	if err != nil {
		return err
	}
	allowed := allowedPrivileges[target.Kind]
	if len(p.Privileges) == 0 {
		return fmt.Errorf("permission for %s has no privileges", p.Target)
	}
	for _, privilege := range p.Privileges {
		if privilege != "ALL" && !allowed[privilege] {
			return fmt.Errorf("privilege %q is not valid for %s", privilege, target.Kind)
		}
	}
	return nil
}

func parsePermissionTarget(value string) (permissionTarget, error) {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		return permissionTarget{}, fmt.Errorf("unsupported permission target %q", value)
	}
	t := permissionTarget{Kind: parts[0]}
	names := strings.Split(parts[1], ".")
	switch t.Kind {
	case "schema":
		if len(names) != 1 {
			return t, fmt.Errorf("invalid schema permission target %q", value)
		}
		t.Schema = names[0]
	case "table", "sequence":
		if len(names) != 2 {
			return t, fmt.Errorf("invalid %s permission target %q", t.Kind, value)
		}
		t.Schema, t.Object = names[0], names[1]
	case "column":
		if len(names) != 3 {
			return t, fmt.Errorf("invalid column permission target %q", value)
		}
		t.Schema, t.Object, t.Column = names[0], names[1], names[2]
	default:
		return t, fmt.Errorf("unsupported permission target kind %q", t.Kind)
	}
	return t, nil
}

func reconcileSecurity(ctx context.Context, db *sql.DB, scope string, spec securitySpec) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin security reconciliation: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// GRANT/REVOKE take strong locks on the targeted objects; bound the wait so
	// reconciliation cannot camp against live traffic (the caller retries).
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '"+migrationLockTimeout+"'"); err != nil {
		return fmt.Errorf("set lock_timeout for security reconciliation: %w", err)
	}

	previous, err := readSecurityState(ctx, tx, scope)
	if err != nil {
		return err
	}
	if err := reconcileRoles(ctx, tx, spec.Roles); err != nil {
		return err
	}
	desiredMemberships := roleMemberships(spec.Roles)
	if err := reconcileMemberships(ctx, tx, previous.Memberships, desiredMemberships); err != nil {
		return err
	}
	desiredPermissions, err := normalizePermissions(spec.Permissions)
	if err != nil {
		return err
	}
	if err := reconcilePermissions(ctx, tx, previous.Permissions, desiredPermissions); err != nil {
		return err
	}
	state := managedSecurityState{Permissions: desiredPermissions, Memberships: desiredMemberships}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal security state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_migration_security(scope, state, updated_at)
VALUES ($1, $2::jsonb, now())
ON CONFLICT (scope) DO UPDATE SET state = EXCLUDED.state, updated_at = now()`, scope, string(data)); err != nil {
		return fmt.Errorf("save security state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit security reconciliation: %w", err)
	}
	return nil
}

func readSecurityState(ctx context.Context, tx *sql.Tx, scope string) (managedSecurityState, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT state FROM schema_migration_security WHERE scope = $1`, scope).Scan(&raw)
	if err == sql.ErrNoRows {
		return managedSecurityState{}, nil
	}
	if err != nil {
		return managedSecurityState{}, fmt.Errorf("read security state: %w", err)
	}
	var state managedSecurityState
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("decode security state: %w", err)
	}
	return state, nil
}

func reconcileRoles(ctx context.Context, tx *sql.Tx, roles []roleSpec) error {
	for _, role := range roles {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role.Name).Scan(&exists); err != nil {
			return fmt.Errorf("inspect role %q: %w", role.Name, err)
		}
		if role.External {
			if !exists {
				return fmt.Errorf("external role %q does not exist", role.Name)
			}
			continue
		}
		quoted := pq.QuoteIdentifier(role.Name)
		if !exists {
			if _, err := tx.ExecContext(ctx, "CREATE ROLE "+quoted); err != nil {
				return fmt.Errorf("create role %q: %w", role.Name, err)
			}
		}
		attributes := []string{
			boolKeyword(role.Superuser, "SUPERUSER"),
			boolKeyword(role.CreateDB, "CREATEDB"),
			boolKeyword(role.CreateRole, "CREATEROLE"),
			boolKeyword(role.Inherit, "INHERIT"),
			boolKeyword(role.Login, "LOGIN"),
			boolKeyword(role.Replication, "REPLICATION"),
			boolKeyword(role.BypassRLS, "BYPASSRLS"),
			fmt.Sprintf("CONNECTION LIMIT %d", role.ConnLimit),
		}
		if _, err := tx.ExecContext(ctx, "ALTER ROLE "+quoted+" WITH "+strings.Join(attributes, " ")); err != nil {
			return fmt.Errorf("alter role %q: %w", role.Name, err)
		}
		comment := "NULL"
		if role.Comment != "" {
			comment = pq.QuoteLiteral(role.Comment)
		}
		if _, err := tx.ExecContext(ctx, "COMMENT ON ROLE "+quoted+" IS "+comment); err != nil {
			return fmt.Errorf("comment role %q: %w", role.Name, err)
		}
	}
	return nil
}

func boolKeyword(value bool, enabled string) string {
	if value {
		return enabled
	}
	return "NO" + enabled
}

func roleMemberships(roles []roleSpec) []membershipSpec {
	var memberships []membershipSpec
	for _, role := range roles {
		if role.External {
			continue
		}
		for _, parent := range role.MemberOf {
			memberships = append(memberships, membershipSpec{Member: role.Name, Parent: parent})
		}
	}
	sort.Slice(memberships, func(i, j int) bool {
		return memberships[i].Member+"\x00"+memberships[i].Parent < memberships[j].Member+"\x00"+memberships[j].Parent
	})
	return memberships
}

func reconcileMemberships(ctx context.Context, tx *sql.Tx, previous, desired []membershipSpec) error {
	want := map[string]membershipSpec{}
	for _, m := range desired {
		want[m.Member+"\x00"+m.Parent] = m
		if _, err := tx.ExecContext(ctx, "GRANT "+pq.QuoteIdentifier(m.Parent)+" TO "+pq.QuoteIdentifier(m.Member)); err != nil {
			return fmt.Errorf("grant role %q to %q: %w", m.Parent, m.Member, err)
		}
	}
	for _, m := range previous {
		if _, ok := want[m.Member+"\x00"+m.Parent]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, "REVOKE "+pq.QuoteIdentifier(m.Parent)+" FROM "+pq.QuoteIdentifier(m.Member)); err != nil {
			return fmt.Errorf("revoke role %q from %q: %w", m.Parent, m.Member, err)
		}
	}
	return nil
}

func normalizePermissions(raw []permissionSpec) ([]permissionSpec, error) {
	grouped := map[string]permissionSpec{}
	for _, p := range raw {
		if err := validatePermission(p); err != nil {
			return nil, err
		}
		key := p.Grantee + "\x00" + p.Target
		current := grouped[key]
		current.Grantee, current.Target = p.Grantee, p.Target
		current.Grantable = current.Grantable || p.Grantable
		allowed := allowedPrivileges[strings.SplitN(p.Target, ":", 2)[0]]
		seen := map[string]bool{}
		for _, privilege := range append(current.Privileges, p.Privileges...) {
			if privilege == "ALL" {
				for candidate := range allowed {
					seen[candidate] = true
				}
			} else {
				seen[privilege] = true
			}
		}
		current.Privileges = current.Privileges[:0]
		for privilege := range seen {
			current.Privileges = append(current.Privileges, privilege)
		}
		sort.Strings(current.Privileges)
		grouped[key] = current
	}
	out := make([]permissionSpec, 0, len(grouped))
	for _, p := range grouped {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Grantee+"\x00"+out[i].Target < out[j].Grantee+"\x00"+out[j].Target })
	return out, nil
}

func reconcilePermissions(ctx context.Context, tx *sql.Tx, previous, desired []permissionSpec) error {
	want := map[string]permissionSpec{}
	for _, p := range desired {
		want[p.Grantee+"\x00"+p.Target] = p
		current, err := currentPrivileges(ctx, tx, p)
		if err != nil {
			return err
		}
		desiredSet := map[string]bool{}
		for _, privilege := range p.Privileges {
			desiredSet[privilege] = true
			grantable, exists := current[privilege]
			switch {
			case !exists || p.Grantable && !grantable:
				if err := grantPrivilege(ctx, tx, p, privilege); err != nil {
					return err
				}
			case !p.Grantable && grantable:
				if err := revokeGrantOption(ctx, tx, p, privilege); err != nil {
					return err
				}
			}
		}
		for privilege := range current {
			if !desiredSet[privilege] && allowedPrivileges[strings.SplitN(p.Target, ":", 2)[0]][privilege] {
				if err := revokePrivilege(ctx, tx, p, privilege); err != nil {
					return err
				}
			}
		}
	}
	for _, p := range previous {
		if _, ok := want[p.Grantee+"\x00"+p.Target]; ok {
			continue
		}
		for _, privilege := range p.Privileges {
			if err := revokePrivilege(ctx, tx, p, privilege); err != nil {
				return err
			}
		}
	}
	return nil
}

func currentPrivileges(ctx context.Context, tx *sql.Tx, p permissionSpec) (map[string]bool, error) {
	t, err := parsePermissionTarget(p.Target)
	if err != nil {
		return nil, err
	}
	var query string
	var args []any
	switch t.Kind {
	case "schema":
		query = `SELECT x.privilege_type, x.is_grantable FROM pg_namespace n CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl, acldefault('n', n.nspowner))) x LEFT JOIN pg_roles r ON r.oid = x.grantee WHERE n.nspname = $1 AND COALESCE(r.rolname, 'PUBLIC') = $2`
		args = []any{t.Schema, p.Grantee}
	case "table", "sequence":
		query = `SELECT x.privilege_type, x.is_grantable FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace CROSS JOIN LATERAL aclexplode(COALESCE(c.relacl, acldefault(CASE WHEN c.relkind = 'S' THEN 'S'::"char" ELSE 'r'::"char" END, c.relowner))) x LEFT JOIN pg_roles r ON r.oid = x.grantee WHERE n.nspname = $1 AND c.relname = $2 AND COALESCE(r.rolname, 'PUBLIC') = $3`
		args = []any{t.Schema, t.Object, p.Grantee}
	case "column":
		query = `SELECT x.privilege_type, x.is_grantable FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace CROSS JOIN LATERAL aclexplode(COALESCE(a.attacl, acldefault('c', c.relowner))) x LEFT JOIN pg_roles r ON r.oid = x.grantee WHERE n.nspname = $1 AND c.relname = $2 AND a.attname = $3 AND COALESCE(r.rolname, 'PUBLIC') = $4`
		args = []any{t.Schema, t.Object, t.Column, p.Grantee}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect permissions for %s on %s: %w", p.Grantee, p.Target, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var privilege string
		var grantable bool
		if err := rows.Scan(&privilege, &grantable); err != nil {
			return nil, err
		}
		out[strings.ToUpper(privilege)] = grantable
	}
	return out, rows.Err()
}

func grantPrivilege(ctx context.Context, tx *sql.Tx, p permissionSpec, privilege string) error {
	statement, err := permissionStatement("GRANT", p, privilege)
	if err != nil {
		return err
	}
	if p.Grantable {
		statement += " WITH GRANT OPTION"
	}
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("grant %s on %s to %s: %w", privilege, p.Target, p.Grantee, err)
	}
	return nil
}

func revokePrivilege(ctx context.Context, tx *sql.Tx, p permissionSpec, privilege string) error {
	statement, err := permissionStatement("REVOKE", p, privilege)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("revoke %s on %s from %s: %w", privilege, p.Target, p.Grantee, err)
	}
	return nil
}

func revokeGrantOption(ctx context.Context, tx *sql.Tx, p permissionSpec, privilege string) error {
	statement, err := permissionStatement("REVOKE GRANT OPTION FOR", p, privilege)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("revoke grant option for %s on %s from %s: %w", privilege, p.Target, p.Grantee, err)
	}
	return nil
}

func permissionStatement(action string, p permissionSpec, privilege string) (string, error) {
	t, err := parsePermissionTarget(p.Target)
	if err != nil {
		return "", err
	}
	grantee := pq.QuoteIdentifier(p.Grantee)
	if p.Grantee == "PUBLIC" {
		grantee = "PUBLIC"
	}
	preposition := " TO "
	if strings.HasPrefix(action, "REVOKE") {
		preposition = " FROM "
	}
	var object string
	switch t.Kind {
	case "schema":
		object = "SCHEMA " + pq.QuoteIdentifier(t.Schema)
	case "table":
		object = "TABLE " + pq.QuoteIdentifier(t.Schema) + "." + pq.QuoteIdentifier(t.Object)
	case "sequence":
		object = "SEQUENCE " + pq.QuoteIdentifier(t.Schema) + "." + pq.QuoteIdentifier(t.Object)
	case "column":
		privilege += " (" + pq.QuoteIdentifier(t.Column) + ")"
		object = "TABLE " + pq.QuoteIdentifier(t.Schema) + "." + pq.QuoteIdentifier(t.Object)
	}
	return action + " " + privilege + " ON " + object + preposition + grantee, nil
}
