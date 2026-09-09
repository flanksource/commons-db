package schedules

// Report formats. Everything except the two facet formats is already produced
// by the profile export path (query/render.go), so a scheduled report and a
// downloaded one are the same bytes; the facet formats render a TSX template
// through the facet service.
const (
	FormatFacetHTML = "facet-html"
	FormatFacetPDF  = "facet-pdf"
)

// clickyFormats are rendered by the existing export pipeline.
var clickyFormats = map[string]struct{}{
	"csv":      {},
	"json":     {},
	"ndjson":   {},
	"yaml":     {},
	"markdown": {},
	"html":     {},
	"excel":    {},
	"pdf":      {},
}

func supportedReportFormat(format string) bool {
	if format == FormatFacetHTML || format == FormatFacetPDF {
		return true
	}
	_, ok := clickyFormats[format]
	return ok
}

// isFacetFormat reports whether the format needs the facet renderer rather than
// the export pipeline.
func isFacetFormat(format string) bool {
	return format == FormatFacetHTML || format == FormatFacetPDF
}

// reportExtension is the file extension a rendered report is stored under.
func reportExtension(format string) string {
	switch format {
	case FormatFacetHTML, "html":
		return ".html"
	case FormatFacetPDF, "pdf":
		return ".pdf"
	case "excel":
		return ".xlsx"
	case "markdown":
		return ".md"
	case "ndjson":
		return ".ndjson"
	case "yaml":
		return ".yaml"
	case "csv":
		return ".csv"
	default:
		return ".json"
	}
}

// reportContentType is the MIME type a rendered report is delivered with.
func reportContentType(format string) string {
	switch format {
	case FormatFacetHTML, "html":
		return "text/html"
	case FormatFacetPDF, "pdf":
		return "application/pdf"
	case "excel":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "markdown":
		return "text/markdown"
	case "yaml":
		return "application/yaml"
	case "csv":
		return "text/csv"
	default:
		return "application/json"
	}
}
