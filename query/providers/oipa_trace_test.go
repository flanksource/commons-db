package providers_test

import (
	"net/http"
	"strings"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// tracePayload mirrors the shape of the captured data-intake trace artifacts
// (~/Downloads/trace-data-intake-*.json): a wrapper object with context fields
// and a Traces array whose rows carry a domain-specific `cells` map.
const tracePayload = `{
  "Policy": null,
  "Plan": null,
  "Traces": [
    {
      "@timestamp": "2026-04-19T11:23:40.207Z",
      "cells": {
        "FileID": "1AB6821C-E9AE-4D21-AF9E-C91F53A5BC70",
        "FileStatus": "INVALID",
        "Profile": "Preservation Member Enrollment",
        "Received": "2026-04-19 11:23:40.207 +0000 UTC"
      }
    },
    {
      "@timestamp": "2026-04-19T11:21:37.373Z",
      "cells": {
        "FileID": "9569E5E0-4E54-4CDF-A0AD-CE1FC6F51507",
        "FileStatus": "VALID",
        "Profile": "Credit Life Billing File",
        "Received": "2026-04-19 11:21:37.373 +0000 UTC"
      }
    }
  ]
}`

// This is the end-to-end validation of the data-intake "trace profile" use case: an
// HTTP provider pulls the Traces array (JSONPath), CEL columns flatten the
// nested cells into top-level columns, and the result renders as a table.
var _ = Describe("data-intake trace profile (end-to-end)", func() {
	traceProfile := func(url string) query.Profile {
		return query.Profile{
			Name: "SQL Server trace",
			Provider: query.ProviderConfig{
				Type:    "http",
				Options: map[string]any{"jsonpath": "$.Traces"},
			},
			Query: url,
			Columns: []query.ColumnDef{
				{Name: "FileID", CEL: "row.cells.FileID"},
				{Name: "FileStatus", CEL: "row.cells.FileStatus"},
				{Name: "Profile", CEL: "row.cells.Profile"},
			},
		}
	}

	It("flattens nested trace cells into columns via CEL", func() {
		srv := jsonServer(http.StatusOK, tracePayload)
		defer srv.Close()

		result, err := query.Execute(context.New(), traceProfile(srv.URL))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0]).To(HaveKeyWithValue("FileStatus", "INVALID"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("Profile", "Preservation Member Enrollment"))
		Expect(result.Rows[1]).To(HaveKeyWithValue("FileStatus", "VALID"))
	})

	It("renders the declared columns as CSV", func() {
		srv := jsonServer(http.StatusOK, tracePayload)
		defer srv.Close()

		result, err := query.Execute(context.New(), traceProfile(srv.URL))
		Expect(err).ToNot(HaveOccurred())

		out, err := result.Render(traceProfile(srv.URL).Columns, "csv")
		Expect(err).ToNot(HaveOccurred())

		header := strings.SplitN(out, "\n", 2)[0]
		Expect(header).To(And(ContainSubstring("File"), ContainSubstring("Status"), ContainSubstring("Profile")))
		Expect(out).To(ContainSubstring("INVALID"))
		Expect(out).To(ContainSubstring("Credit Life Billing File"))
	})
})
