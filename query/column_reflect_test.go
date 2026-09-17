package query_test

import (
	"encoding/json"
	"reflect"
	"time"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

type reflectedAudit struct {
	Actor string `json:"actor" filter:"exact"`
}

type reflectedEvent struct {
	reflectedAudit
	Name       string            `json:"name" pretty:"label=Event" sort:"name"`
	Database   string            `json:"db" filter:"terms,limit=20"`
	Captured   time.Time         `json:"captured_at" pretty:"label=Captured,format=date"`
	DurationMS float64           `json:"duration_ms" pretty:"type=duration,unit=ms"`
	Reads      int64             `json:"reads" filter:"range"`
	Failed     bool              `json:"failed"`
	Status     string            `json:"status" filter:"options=ok|failed,lookup=false"`
	Level      string            `json:"level" filter:"-"`
	Session    int               `json:"session_id,string"`
	Tables     []string          `json:"tables"`
	Detail     map[string]string `json:"detail" pretty:"hide"`
	Payload    json.RawMessage   `json:"payload" pretty:"-"`
	Kept       *time.Time        `json:"kept,omitempty" pretty:"kind=timestamp"`
	Ignored    string            `json:"-"`
	internal   string
}

// String reads the unexported field, which ColumnsFor must skip as
// encoding/json does.
func (e reflectedEvent) String() string { return e.internal }

var _ = Describe("ColumnsFor", func() {
	It("reflects json names, pretty display metadata, sort keys and filter overrides into columns", func() {
		columns, err := query.ColumnsFor(reflect.TypeOf(reflectedEvent{}))
		Expect(err).ToNot(HaveOccurred())
		Expect(columns).To(Equal([]query.ColumnDef{
			{Name: "actor", Type: query.ColumnTypeString, Filter: &query.ColumnFilterDef{Kind: query.ColumnFilterKindExact}},
			{Name: "name", Label: "Event", Type: query.ColumnTypeString},
			{Name: "db", Type: query.ColumnTypeString, Filter: &query.ColumnFilterDef{Kind: query.ColumnFilterKindTerms, Limit: lo.ToPtr(20)}},
			{Name: "captured_at", Label: "Captured", Type: query.ColumnTypeDateTime, Format: "date"},
			{Name: "duration_ms", Type: query.ColumnTypeDuration, Unit: "ms"},
			{Name: "reads", Type: query.ColumnTypeNumber, Format: "integer", Filter: &query.ColumnFilterDef{Kind: query.ColumnFilterKindRange}},
			{Name: "failed", Type: query.ColumnTypeBoolean},
			{Name: "status", Type: query.ColumnTypeString, Filter: &query.ColumnFilterDef{Options: []string{"ok", "failed"}, Lookup: lo.ToPtr(false)}},
			{Name: "level", Type: query.ColumnTypeString, Filter: &query.ColumnFilterDef{Disabled: true}},
			{Name: "session_id", Type: query.ColumnTypeString},
			{Name: "tables", Type: query.ColumnTypeJSON, Filter: &query.ColumnFilterDef{Kind: query.ColumnFilterKindTerms, Array: true}},
			{Name: "detail", Type: query.ColumnTypeJSON, Hidden: true},
			{Name: "payload", Type: query.ColumnTypeJSON, Hidden: true},
			{Name: "kept", Type: query.ColumnTypeDateTime, Kind: query.ColumnKindTimestamp},
		}))
	})

	It("accepts a pointer to the row struct", func() {
		columns, err := query.ColumnsFor(reflect.TypeOf(&reflectedAudit{}))
		Expect(err).ToNot(HaveOccurred())
		Expect(columns).To(HaveLen(1))
	})

	DescribeTable("refuses a tag or field it cannot turn into a column",
		func(row any, message string) {
			_, err := query.ColumnsFor(reflect.TypeOf(row))
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a row that is not a struct", "", "must be a struct"),
		Entry("an unknown pretty key", struct {
			A string `json:"a" pretty:"colour=red"`
		}{}, `pretty key "colour"`),
		Entry("an unknown pretty flag", struct {
			A string `json:"a" pretty:"sparkle"`
		}{}, `pretty flag "sparkle"`),
		Entry("an unknown column type", struct {
			A string `json:"a" pretty:"type=money"`
		}{}, `type "money"`),
		Entry("an unknown column kind", struct {
			A string `json:"a" pretty:"kind=clock"`
		}{}, `kind "clock"`),
		Entry("a unit clicky does not render", struct {
			A float64 `json:"a" pretty:"unit=furlongs"`
		}{}, `unit "furlongs"`),
		Entry("a unit on a column that is not numeric", struct {
			A string `json:"a" pretty:"unit=ms"`
		}{}, "unit requires type number"),
		Entry("an unknown filter key", struct {
			A string `json:"a" filter:"nested=tags"`
		}{}, `filter key "nested"`),
		Entry("an unknown filter flag", struct {
			A string `json:"a" filter:"fuzzy"`
		}{}, `filter flag "fuzzy"`),
		Entry("two filter kinds", struct {
			A string `json:"a" filter:"terms,exact"`
		}{}, "declares two filter kinds"),
		Entry("options on a range filter", struct {
			A int `json:"a" filter:"range,options=1|2"`
		}{}, "filter options require"),
		Entry("a limit that is not a number", struct {
			A string `json:"a" filter:"limit=many"`
		}{}, `filter limit "many"`),
		Entry("a boolean that is not one", struct {
			A string `json:"a" filter:"lookup=maybe"`
		}{}, `filter lookup "maybe"`),
		Entry("a sort key that is not the column name", struct {
			A string `json:"a" sort:"alpha"`
		}{}, `sort key "alpha"`),
		Entry("a sort key on a hidden column", struct {
			A string `json:"a" sort:"a" pretty:"hide"`
		}{}, "hidden"),
		Entry("a time.Duration, which JSON encodes as nanoseconds", struct {
			A time.Duration `json:"a"`
		}{}, "nanoseconds"),
		Entry("a field JSON cannot encode", struct {
			A chan int `json:"a"`
		}{}, "chan"),
		Entry("an embedded field and an outer one with one json name", struct {
			reflectedAudit
			Actor string `json:"actor"`
		}{}, `column "actor" is declared twice`),
	)
})
