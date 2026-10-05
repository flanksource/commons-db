// Specs for the row processing a kind opts into before its records are stored:
// truncation, embedded JSON parsing, secret masking, and their order.

package traces

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
)

var _ = Describe("Row processors", func() {
	Describe("truncateStrings", func() {
		It("cuts long strings at a rune boundary and lists the paths it cut", func() {
			row := recordstore.Row{
				"short": "ok",
				"long":  strings.Repeat("é", 10), // 20 bytes
				"nested": map[string]any{
					"items": []any{"fine", strings.Repeat("x", 12)},
				},
			}
			out := truncateStrings(9, nil)(row)
			Expect(out["short"]).To(Equal("ok"))
			Expect(out["long"]).To(Equal(strings.Repeat("é", 4) + truncationMarker))
			Expect(out["nested"]).To(Equal(map[string]any{
				"items": []any{"fine", strings.Repeat("x", 9) + truncationMarker},
			}))
			Expect(out[TruncatedColumn]).To(Equal([]any{"long", "nested.items.1"}))
		})

		It("never cuts a column whose value has a fixed form, such as a time", func() {
			out := truncateStrings(4, []string{"at"})(recordstore.Row{"at": "2026-10-05T12:00:00Z", "note": "long note"})
			Expect(out["at"]).To(Equal("2026-10-05T12:00:00Z"))
			Expect(out["note"]).To(Equal("long" + truncationMarker))
			Expect(out[TruncatedColumn]).To(Equal([]any{"note"}))
		})

		It("leaves a row with nothing too long as it was", func() {
			out := truncateStrings(10, nil)(recordstore.Row{"a": "short", "n": json.Number("12")})
			Expect(out).To(Equal(recordstore.Row{"a": "short", "n": json.Number("12")}))
		})
	})

	Describe("parseEmbeddedJSON", func() {
		It("decodes strings holding a JSON object or array, keeping numbers exact", func() {
			out := parseEmbeddedJSON(recordstore.Row{
				"object":  `{"a": 12345678901234567890}`,
				"array":   ` [1, "two"] `,
				"invalid": `{"a":`,
				"plain":   "not json",
				"scalar":  "42",
				"nested":  map[string]any{"body": `{"ok": true}`},
			})
			Expect(out["object"]).To(Equal(map[string]any{"a": json.Number("12345678901234567890")}))
			Expect(out["array"]).To(Equal([]any{json.Number("1"), "two"}))
			Expect(out["invalid"]).To(Equal(`{"a":`))
			Expect(out["plain"]).To(Equal("not json"))
			Expect(out["scalar"]).To(Equal("42"))
			Expect(out["nested"]).To(Equal(map[string]any{"body": map[string]any{"ok": true}}))
		})
	})

	Describe("maskSecrets", func() {
		It("masks sensitive keys at any depth, name/value pairs, and secrets inside strings", func() {
			out := maskSecrets(nil)(recordstore.Row{
				"password": "hunter2",
				"status":   "ok",
				"request": map[string]any{
					"headers": []any{
						map[string]any{"name": "Authorization", "value": "Bearer abc.def.ghi"},
						map[string]any{"name": "Accept", "value": "application/json"},
					},
					"url": "https://api.example.com/v1/items?token=s3cr3t-value-here&page=2",
				},
				"body": map[string]any{"client_secret": "abc", "count": json.Number("2")},
			})
			Expect(out["password"]).To(Equal(maskedValue))
			Expect(out["status"]).To(Equal("ok"))
			request := out["request"].(map[string]any)
			headers := request["headers"].([]any)
			Expect(headers[0]).To(Equal(map[string]any{"name": "Authorization", "value": maskedValue}))
			Expect(headers[1]).To(Equal(map[string]any{"name": "Accept", "value": "application/json"}))
			Expect(request["url"]).ToNot(ContainSubstring("s3cr3t-value-here"))
			Expect(request["url"]).To(ContainSubstring("page=2"))
			Expect(out["body"]).To(Equal(map[string]any{"client_secret": maskedValue, "count": json.Number("2")}))
		})

		It("keeps the keys it is told to keep, at any depth", func() {
			out := maskSecrets([]string{"username"})(recordstore.Row{
				"username": "sa",
				"password": "hunter2",
				"children": []any{map[string]any{"username": "sa", "password": "hunter2"}},
			})
			Expect(out["username"]).To(Equal("sa"))
			Expect(out["password"]).To(Equal(maskedValue))
			Expect(out["children"]).To(Equal([]any{map[string]any{"username": "sa", "password": maskedValue}}))
		})
	})

	It("processes in order: truncate, then parse JSON, then mask", func() {
		process := pipeline([]rowProcessor{truncateStrings(64, nil), parseEmbeddedJSON, maskSecrets(nil)})
		out := process(recordstore.Row{
			"body":  `{"password": "hunter2", "user": "bob"}`,
			"large": `{"password": "` + strings.Repeat("p", 100) + `"}`,
		})
		Expect(out["body"]).To(Equal(map[string]any{"password": maskedValue, "user": "bob"}))
		Expect(out["large"]).To(BeAssignableToTypeOf(""))
		Expect(out["large"]).ToNot(ContainSubstring(strings.Repeat("p", 100)))
		Expect(out[TruncatedColumn]).To(Equal([]any{"large"}))
	})
})
