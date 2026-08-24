package providers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers"
)

// The container rules are measured against a real cluster rather than asserted
// against a fixture, because every one of them is a claim about what OpenSearch
// does with a clause — and the failure they exist to prevent is silent. A flat
// clause on a nested field returns zero hits and no error; a pair of clauses on
// an array of objects returns the wrong documents and no error. A stub would
// agree with whatever this package sent it.
//
// Set COMMONS_DB_OPENSEARCH_URL to run these. Credentials travel in the URL:
// http://admin:secret@localhost:9200
var _ = Describe("opensearch containers (live)", Ordered, func() {
	const index = "commons-db-nested-it"

	var (
		address string
		ctx     dbcontext.Context
	)

	profileFor := func(column query.ColumnDef) query.Profile {
		return query.Profile{
			Name: "tags",
			Provider: query.ProviderConfig{
				Type:    "opensearch",
				Options: map[string]any{"address": address, "index": index, "limit": "10"},
			},
			Query:   `{"query":{"match_all":{}},"sort":[{"_id":{"order":"asc"}}]}`,
			Columns: []query.ColumnDef{{Name: "id", Source: "id"}, column},
		}
	}

	idsOf := func(rows []query.Row) []string {
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, fmt.Sprint(row["id"]))
		}
		return ids
	}

	BeforeAll(func() {
		address = strings.TrimSpace(os.Getenv("COMMONS_DB_OPENSEARCH_URL"))
		if address == "" {
			Skip("COMMONS_DB_OPENSEARCH_URL is not set")
		}
		ctx = dbcontext.New()

		// tags is nested and otel is not, and they carry identical documents: the
		// difference between them is the whole subject of these tests.
		Expect(openSearchAdmin(address, http.MethodDelete, "/"+index, "")).To(Or(Succeed(), Succeed()))
		Expect(openSearchAdmin(address, http.MethodPut, "/"+index, `{"mappings":{"properties":{
			"id":{"type":"keyword"},
			"tags":{"type":"nested","properties":{"key":{"type":"keyword"},"value":{"type":"keyword"}}},
			"otel":{"properties":{"key":{"type":"keyword"},"value":{"type":"keyword"}}},
			"attrs":{"type":"flat_object"}
		}}}`)).To(Succeed())
		Expect(openSearchAdmin(address, http.MethodPost, "/_bulk?refresh=true", strings.Join([]string{
			`{"index":{"_index":"` + index + `","_id":"1"}}`,
			`{"id":"1","tags":[{"key":"app","value":"web"},{"key":"env","value":"prod"}],` +
				`"otel":[{"key":"app","value":"web"},{"key":"env","value":"prod"}],` +
				`"attrs":{"app":"web","env":"prod"}}`,
			`{"index":{"_index":"` + index + `","_id":"2"}}`,
			`{"id":"2","tags":[{"key":"app","value":"api"},{"key":"env","value":"dev"}],` +
				`"otel":[{"key":"app","value":"api"},{"key":"env","value":"dev"}],` +
				`"attrs":{"app":"api","env":"dev"}}`,
			"",
		}, "\n"))).To(Succeed())
	})

	AfterAll(func() {
		if address != "" {
			_ = openSearchAdmin(address, http.MethodDelete, "/"+index, "")
		}
	})

	It("reads which container each leaf belongs to from the mapping", func() {
		searcher, err := opensearch.New(ctx, opensearch.Backend{Address: address}, nil)
		Expect(err).ToNot(HaveOccurred())
		inspector, err := opensearchinspect.New(searcher.GetRawClient(), opensearchinspect.Options{})
		Expect(err).ToNot(HaveOccurred())
		catalog, err := inspector.Fields(ctx, opensearchinspect.FieldRequest{
			Target: opensearchinspect.Target{Name: index, Kind: "index"},
		})
		Expect(err).ToNot(HaveOccurred())

		byName := map[string]opensearchinspect.Field{}
		for _, field := range catalog.Fields {
			byName[field.Name] = field
		}
		Expect(byName["tags.value"].ContainerType).To(Equal(opensearchinspect.ContainerNested))
		Expect(byName["otel.value"].ContainerType).To(Equal(opensearchinspect.ContainerObject))
		// A flat_object reports its root and nothing under it, which is why its
		// sub-keys are discovered from documents instead.
		Expect(byName).ToNot(HaveKey("attrs.app"))
		Expect(byName["attrs"].Types).To(Equal([]string{opensearchinspect.ContainerFlatObject}))
	})

	It("selects the documents whose picked entry carries the value", func() {
		profile := profileFor(query.ColumnDef{
			Name: "app", JSONPath: "$.tags[?(@.key == 'app')].value",
			Filter: &query.ColumnFilterDef{Nested: "tags"},
		})
		result, err := query.Execute(ctx, profile, map[string]any{"filter.app": "web"})
		Expect(err).ToNot(HaveOccurred())
		Expect(idsOf(result.Rows)).To(Equal([]string{"1"}))
	})

	// The clause that would be compiled without the nested wrapper. Document 1 is
	// tagged app=web and env=prod, so a document-level AND of key=app and
	// value=prod matches it — the wrong document, returned with no complaint.
	It("does not match the document a flat pair of clauses would", func() {
		profile := profileFor(query.ColumnDef{
			Name: "app", JSONPath: "$.tags[?(@.key == 'app')].value",
			Filter: &query.ColumnFilterDef{Nested: "tags"},
		})
		result, err := query.Execute(ctx, profile, map[string]any{"filter.app": "prod"})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(BeEmpty())

		flat, err := providers.FilterOpenSearch(`{"query":{"match_all":{}}}`, []query.ColumnFilterValue{
			{Field: "otel.key", Kind: query.ColumnFilterKindTerms, Include: []string{"app"}},
			{Field: "otel.value", Kind: query.ColumnFilterKindTerms, Include: []string{"prod"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(openSearchHitIDs(address, index, flat)).To(Equal([]string{"1"}),
			"this is the wrong answer a plain object array gives, and why its leaves offer no filter")
	})

	It("excludes every document carrying the picked entry", func() {
		profile := profileFor(query.ColumnDef{
			Name: "app", JSONPath: "$.tags[?(@.key == 'app')].value",
			Filter: &query.ColumnFilterDef{Nested: "tags"},
		})
		result, err := query.Execute(ctx, profile, map[string]any{"filter.app": "!web"})
		Expect(err).ToNot(HaveOccurred())
		Expect(idsOf(result.Rows)).To(Equal([]string{"2"}))
	})

	It("correlates two entries of one container as two selections", func() {
		profile := profileFor(query.ColumnDef{
			Name: "app", JSONPath: "$.tags[?(@.key == 'app')].value",
			Filter: &query.ColumnFilterDef{Nested: "tags"},
		})
		profile.Columns = append(profile.Columns, query.ColumnDef{
			Name: "env", JSONPath: "$.tags[?(@.key == 'env')].value",
			Filter: &query.ColumnFilterDef{Nested: "tags"},
		})
		matched, err := query.Execute(ctx, profile, map[string]any{"filter.app": "web", "filter.env": "prod"})
		Expect(err).ToNot(HaveOccurred())
		Expect(idsOf(matched.Rows)).To(Equal([]string{"1"}))

		crossed, err := query.Execute(ctx, profile, map[string]any{"filter.app": "web", "filter.env": "dev"})
		Expect(err).ToNot(HaveOccurred())
		Expect(crossed.Rows).To(BeEmpty())
	})

	It("offers the values of the picked entry and no others", func() {
		profile := profileFor(query.ColumnDef{
			Name: "app", JSONPath: "$.tags[?(@.key == 'app')].value",
			Filter: &query.ColumnFilterDef{Nested: "tags"},
		})
		options, _, err := query.LookupFilterValues(ctx, query.FilterValueLookupRequest{
			Profile: profile, Key: "filter.app", Limit: 10,
		})
		Expect(err).ToNot(HaveOccurred())
		values := make([]string, 0, len(options))
		for _, option := range options {
			values = append(values, option.Value)
		}
		// "prod" and "dev" are values of the env tag; a lookup that descended
		// without pinning the entry would offer them here.
		Expect(values).To(ConsistOf("web", "api"))
	})

	It("narrows a flat_object sub-key through the jsonpath that reads it", func() {
		profile := profileFor(query.ColumnDef{Name: "app", Source: "attrs", JSONPath: `$["app"]`})
		result, err := query.Execute(ctx, profile, map[string]any{"filter.app": "api"})
		Expect(err).ToNot(HaveOccurred())
		Expect(idsOf(result.Rows)).To(Equal([]string{"2"}))
	})
})

// openSearchAdmin issues an index-management request. It talks to the cluster
// directly because creating the fixture is not something the query layer does.
func openSearchAdmin(address, method, path, body string) error {
	request, err := http.NewRequest(method, strings.TrimRight(address, "/")+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if strings.HasSuffix(path, "_bulk?refresh=true") {
		request.Header.Set("Content-Type", "application/x-ndjson")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	// Deleting an index that is not there is how each run starts, so it is the
	// state being asked for rather than a failure to reach it.
	absent := method == http.MethodDelete && response.StatusCode == http.StatusNotFound
	if response.StatusCode >= 300 && !absent {
		return fmt.Errorf("%s %s: %s", method, path, response.Status)
	}
	return nil
}

// openSearchHitIDs runs a compiled body and returns the ids it matched, so a
// test can state what a clause selects rather than what it looks like.
func openSearchHitIDs(address, index, body string) ([]string, error) {
	request, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(address, "/")+"/"+index+"/_search", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	var decoded struct {
		Hits struct {
			Hits []struct {
				ID string `json:"_id"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(decoded.Hits.Hits))
	for _, hit := range decoded.Hits.Hits {
		ids = append(ids, hit.ID)
	}
	return ids, nil
}
