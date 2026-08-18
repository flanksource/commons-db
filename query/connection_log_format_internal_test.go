package query

import (
	"strings"
	"time"

	clickytext "github.com/flanksource/clicky/text"
	"github.com/flanksource/commons-db/observability"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Connection log formatting", func() {
	It("renders request filters on one deterministic line", func() {
		diagnostics := NewProviderDiagnostics("k8s", "kind=Pod namespace=prod", nil)
		diagnostics.RecordRequest("kind=Pod namespace=prod", nil, map[string]any{
			"pods":  []string{"prod/billing-2"},
			"start": "2026-04-19T11:00:00Z",
			"end":   "2026-04-19T12:00:00Z",
		})
		operation := connectionOperation{
			policy:      observability.Policy{Family: observability.ProviderHTTP},
			diagnostics: diagnostics, provider: "k8s", connection: "kube",
		}
		pretty := operation.prettySummary(observability.EventSlow, 4385*time.Millisecond, 1239, nil)
		ansi := strings.ReplaceAll(clickytext.StripANSI(pretty.ANSI()), "\u00a0", " ")

		Expect(ansi).To(And(
			ContainSubstring("[k8s/kube] SLOW >= [4385ms] [rows:1239]"),
			ContainSubstring("kind=Pod namespace=prod filters=end: 2026-04-19T12:00:00Z, pods: [prod/billing-2], start: 2026-04-19T11:00:00Z"),
			Not(ContainSubstring("\n")),
		))
		Expect(pretty.HTML()).To(And(
			Not(ContainSubstring("<dl")),
			ContainSubstring("prod/billing-2"),
			ContainSubstring("2026-04-19T11:00:00Z"),
		))
	})

	It("renders arguments containing newlines on one line", func() {
		operation := connectionOperation{provider: "k8s", connection: "kube"}
		pretty := operation.prettyParams([]any{"start: now-1w\nend: now", []string{"acme", "tenant-x"}})
		ansi := strings.ReplaceAll(clickytext.StripANSI(pretty.ANSI()), "\u00a0", " ")

		Expect(ansi).To(ContainSubstring("params [start: now-1w end: now [acme tenant-x]]"))
		Expect(ansi).ToNot(ContainSubstring("\n"))
	})
})
