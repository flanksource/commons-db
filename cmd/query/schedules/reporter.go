package schedules

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons-db/cmd/query/schedules/report"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// reporter renders a result to report bytes.
//
// Everything except the facet formats goes through the same Result.Render the
// profile export already uses, so a scheduled CSV and a downloaded one are byte
// for byte the same file. Only facet-html and facet-pdf need the renderer.
type reporter struct{}

// NewReporter returns the default report renderer.
func NewReporter() Reporter { return reporter{} }

func (reporter) Render(
	ctx dbcontext.Context,
	spec ReportSpec,
	result *query.Result,
	columns []query.ColumnDef,
) ([]byte, error) {
	if result == nil {
		return nil, fmt.Errorf("cannot render a report from no result")
	}
	if isFacetFormat(spec.Format) {
		return renderFacet(ctx, spec, result, columns)
	}

	rendered, err := result.Render(columns, spec.Format)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", spec.Format, err)
	}
	return []byte(rendered), nil
}

func renderFacet(
	ctx dbcontext.Context,
	spec ReportSpec,
	result *query.Result,
	columns []query.ColumnDef,
) ([]byte, error) {
	options := report.Options{}
	if spec.Facet != nil {
		options = report.Options{
			Connection:   spec.Facet.Connection,
			URL:          spec.Facet.URL,
			Timeout:      time.Duration(spec.Facet.Timeout),
			TimestampURL: spec.Facet.TimestampURL,
		}
	}
	server, err := report.ResolveServer(ctx, options)
	if err != nil {
		return nil, err
	}

	srcDir, err := report.TemplateDir()
	if err != nil {
		return nil, err
	}
	entryFile := report.EntryFile
	if strings.TrimSpace(spec.Template) != "" {
		entryFile = spec.Template
	}

	// facet's own format names drop the prefix the schedule spec uses to
	// distinguish them from the export pipeline's html and pdf.
	format := strings.TrimPrefix(spec.Format, "facet-")
	payload := report.NewPayload(spec.Title, result, columns)

	return report.Render(ctx, server, payload, format, srcDir, entryFile)
}
