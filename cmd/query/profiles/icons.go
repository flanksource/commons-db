package profiles

// providerIcon maps a profile's provider type to an opaque UI icon name. The
// names are the contract with clicky-ui's surfaceIconMap (which resolves them to
// glyph components); keep the two in sync. Unknown providers fall back to a
// generic table icon.
func providerIcon(providerType string) string {
	switch providerType {
	case "sql", "postgres", "mysql", "sqlserver", "clickhouse":
		return "database"
	case "http", "postgrest":
		return "globe"
	case "prometheus":
		return "graph"
	case "loki":
		return "activity"
	case "opensearch":
		return "globe"
	default:
		return "table"
	}
}
