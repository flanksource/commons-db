package types

import "testing"

func TestEnvVarScanStringRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantKind string // which ValueFrom source is expected, "" for a static literal
	}{
		{name: "secret", raw: "secret://db-creds/password", wantKind: "secret"},
		{name: "configmap", raw: "configmap://app-config/host", wantKind: "configmap"},
		{name: "helm", raw: "helm://my-release/db.password", wantKind: "helm"},
		{name: "service account", raw: "serviceaccount://reader", wantKind: "serviceaccount"},
		{name: "1password field", raw: "op://prod/postgres/password", wantKind: "onepassword"},
		{name: "1password sectioned", raw: "op://prod/postgres/section/password", wantKind: "onepassword"},
		{name: "literal", raw: "postgres://localhost:5432/db", wantKind: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e EnvVar
			if err := e.Scan(tc.raw); err != nil {
				t.Fatalf("Scan(%q) failed: %v", tc.raw, err)
			}
			if got := gotKind(e); got != tc.wantKind {
				t.Fatalf("Scan(%q) resolved to kind %q, want %q", tc.raw, got, tc.wantKind)
			}
			if got := e.String(); got != tc.raw {
				t.Fatalf("round-trip mismatch: Scan(%q).String() = %q", tc.raw, got)
			}
		})
	}
}

func TestEnvVarScanRejectsMalformedOnePassword(t *testing.T) {
	// A 1Password reference needs a vault, item and field after the scheme.
	var e EnvVar
	if err := e.Scan("op://vault/item"); err == nil {
		t.Fatal("Scan(op://vault/item) should reject a reference missing a field")
	}
}

// gotKind reports which ValueFrom source an EnvVar carries after a Scan.
func gotKind(e EnvVar) string {
	if e.ValueFrom == nil {
		return ""
	}
	switch {
	case e.ValueFrom.SecretKeyRef != nil:
		return "secret"
	case e.ValueFrom.ConfigMapKeyRef != nil:
		return "configmap"
	case e.ValueFrom.HelmRef != nil:
		return "helm"
	case e.ValueFrom.ServiceAccount != nil:
		return "serviceaccount"
	case e.ValueFrom.OnePassword != nil:
		return "onepassword"
	default:
		return ""
	}
}
