package connections

import (
	"encoding/json"
	"net/http"
	"strings"
)

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(payload)
}

func MaskValue(value string) string {
	const bullet = "••••"
	runes := []rune(strings.TrimSpace(value))
	switch {
	case len(runes) == 0:
		return ""
	case len(runes) <= 8:
		return bullet
	default:
		return string(runes[:4]) + bullet + string(runes[len(runes)-4:])
	}
}
