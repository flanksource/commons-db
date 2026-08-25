package schema_test

import (
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Profile parameter types", func() {
	It("advertises temporal and SQL identifier types with their editor metadata", func() {
		params := schema.Profile()["properties"].(schema.Schema)["params"].(schema.Schema)
		typeProperty := params["items"].(schema.Schema)["properties"].(schema.Schema)["type"].(schema.Schema)

		Expect(typeProperty["enum"]).To(ContainElements("date", "datetime", "duration", "identifier"))
		Expect(typeProperty["x-enum-labels"]).To(Equal(map[string]string{
			"datetime":   "Date & time",
			"duration":   "Duration",
			"identifier": "SQL identifier",
			"list":       "List (multi-select)",
			"labels":     "Kubernetes labels",
		}))
		Expect(typeProperty["x-enum-icons"]).To(Equal(map[string]string{
			"string": "cursor-text", "number": "sigma", "boolean": "toggle-on",
			"date": "calendar", "datetime": "clock", "duration": "timer",
			"enum": "tag", "identifier": "database", "list": "list-dashes", "labels": "tags",
		}))
		Expect(typeProperty["x-enum-tones"]).To(Equal(map[string]string{
			"string": "slate", "number": "violet", "boolean": "amber",
			"date": "sky", "datetime": "rose", "duration": "neutral",
			"enum": "teal", "identifier": "sky", "list": "indigo", "labels": "emerald",
		}))
	})

	It("uses distinct JSON Schema contracts for date, datetime, and duration values", func() {
		instance, err := schema.ProfileInstance(query.Profile{
			Name: "temporal",
			Params: []query.ParamDef{
				{Name: "day", Type: query.ParamTypeDate},
				{Name: "started_at", Type: query.ParamTypeDateTime},
				{Name: "window", Type: query.ParamTypeDuration},
			},
		})
		Expect(err).ToNot(HaveOccurred())

		properties := instance["properties"].(schema.Schema)
		Expect(properties["day"]).To(Equal(schema.Schema{"type": "string", "title": "day", "format": "date"}))
		Expect(properties["started_at"]).To(Equal(schema.Schema{"type": "string", "title": "started_at", "format": "date-time"}))
		Expect(properties["window"]).To(Equal(schema.Schema{"type": "string", "title": "window"}))
	})
})
