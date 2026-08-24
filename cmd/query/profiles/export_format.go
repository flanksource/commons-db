package profiles

// Export-format negotiation: which representation a caller asked for, what to
// call the file, and what to answer with. Split out of execution.go, which owns
// the routing and the execution itself.

import (
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

func supportedExportFormat(format string) bool {
	switch format {
	case "clicky-json", "json", "ndjson", "yaml", "csv", "markdown", "html", "excel", "pdf":
		return true
	default:
		return false
	}
}

func requestedFormat(r *http.Request) string {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "xlsx":
		return "excel"
	case "md":
		return "markdown"
	case "yml":
		return "yaml"
	case "":
		return acceptedFormat(r.Header.Get("Accept"))
	default:
		return format
	}
}

// acceptedFormat picks the format an Accept header asks for.
//
// Accept is ranked, not ordered: a caller listing text/html first at q=0.1 and
// the clicky envelope at q=0.9 is asking for the envelope. Reading the first
// recognised entry answers with the one it weighted lowest — and a q=0 is a
// refusal, not a low preference. Ties keep the earlier entry, which is the
// order the caller wrote them in.
func acceptedFormat(accept string) string {
	best, bestQuality := "json", -1.0
	for _, part := range strings.Split(accept, ",") {
		fields := strings.Split(part, ";")
		format, ok := formatForMediaType(strings.ToLower(strings.TrimSpace(fields[0])))
		if !ok {
			continue
		}
		quality := 1.0
		for _, parameter := range fields[1:] {
			name, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !found || strings.ToLower(strings.TrimSpace(name)) != "q" {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil {
				continue
			}
			quality = parsed
		}
		if quality <= 0 || quality <= bestQuality {
			continue
		}
		best, bestQuality = format, quality
	}
	return best
}

func formatForMediaType(media string) (string, bool) {
	switch media {
	case "application/json+clicky", "application/clicky+json":
		return "clicky-json", true
	case "application/x-ndjson", "application/ndjson":
		return "ndjson", true
	case "application/yaml", "application/x-yaml", "text/yaml":
		return "yaml", true
	case "text/csv", "application/csv":
		return "csv", true
	case "text/markdown":
		return "markdown", true
	case "text/html":
		return "html", true
	case "application/pdf":
		return "pdf", true
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "excel", true
	case "application/json":
		return "json", true
	default:
		return "", false
	}
}

func isTabularExport(format string) bool {
	switch format {
	case "csv", "markdown", "html", "excel", "pdf":
		return true
	default:
		return false
	}
}

func exportContentType(format string) string {
	switch format {
	case "clicky-json":
		return "application/json+clicky"
	case "json":
		return "application/json"
	case "ndjson":
		return "application/x-ndjson"
	case "yaml":
		return "application/yaml"
	case "csv":
		return "text/csv; charset=utf-8"
	case "markdown":
		return "text/markdown; charset=utf-8"
	case "html":
		return "text/html; charset=utf-8"
	case "excel":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

func exportExtension(format string) string {
	switch format {
	case "markdown":
		return ".md"
	case "excel":
		return ".xlsx"
	case "ndjson":
		return ".ndjson"
	default:
		return "." + format
	}
}

func sanitizeExportFilename(filename string) string {
	filename = strings.ReplaceAll(filename, "\\", "/")
	parts := strings.Split(filename, "/")
	filename = parts[len(parts)-1]
	filename = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`\";`, r) {
			return '_'
		}
		return r
	}, filename)
	filename = strings.Trim(filename, " .")
	if filename == "" {
		return "query-export.json"
	}
	return filename
}
