package sqlinspect

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQLite schema metadata", func() {
	It("captures nullability, defaults, indexes, foreign keys, views, and triggers", func(ctx SpecContext) {
		client, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "source.db"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(client.Close)
		for _, statement := range []string{
			"CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
			"CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER NOT NULL REFERENCES customers(id), status TEXT DEFAULT 'new')",
			"CREATE UNIQUE INDEX orders_customer_idx ON orders(customer_id)",
			"CREATE VIEW order_status AS SELECT status FROM orders",
			"CREATE TRIGGER orders_audit AFTER INSERT ON orders BEGIN SELECT 1; END",
		} {
			_, err := client.ExecContext(ctx, statement)
			Expect(err).ToNot(HaveOccurred())
		}
		catalog, err := Inspect(ctx, client, "sqlite", Options{CacheKey: "sqlite-schema-metadata", Refresh: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(catalog.Truncated).To(BeFalse())
		var orders, view Relation
		for _, relation := range catalog.Schemas[0].Relations {
			if relation.Name == "orders" {
				orders = relation
			}
			if relation.Name == "order_status" {
				view = relation
			}
		}
		Expect(orders.Columns).To(HaveLen(3))
		Expect(orders.Columns[1].Nullable).To(Equal(pointer(false)))
		Expect(orders.Columns[2].Default).To(Equal(pointer("'new'")))
		Expect(orders.Indexes).To(ContainElement(And(HaveField("Name", "orders_customer_idx"), HaveField("Unique", true))))
		Expect(orders.ForeignKeys).To(ContainElement(And(HaveField("ReferencedTable", "customers"), HaveField("Columns", []string{"customer_id"}))))
		Expect(orders.Triggers).To(ContainElement(HaveField("Name", "orders_audit")))
		Expect(view.ViewDef).To(ContainSubstring("SELECT status FROM orders"))
	})

	It("keeps column and constraint limits independent", func(ctx SpecContext) {
		client, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "bounded.db"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(client.Close)
		_, err = client.ExecContext(ctx, "CREATE TABLE items (id INTEGER, name TEXT)")
		Expect(err).ToNot(HaveOccurred())
		_, err = client.ExecContext(ctx, "CREATE INDEX items_name_idx ON items(name)")
		Expect(err).ToNot(HaveOccurred())
		catalog, err := Inspect(ctx, client, "sqlite", Options{CacheKey: "sqlite-independent-limits", Refresh: true, Limits: Limits{MaxColumns: 2}})
		Expect(err).ToNot(HaveOccurred())
		Expect(catalog.Truncated).To(BeFalse())
		Expect(catalog.Schemas[0].Relations[0].Indexes).To(HaveLen(1))
	})

	It("preserves SQLite primary-key nullability and the catalog column limit", func(ctx SpecContext) {
		client, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "columns.db"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(client.Close)
		for _, statement := range []string{
			"CREATE TABLE rowid_key (code TEXT PRIMARY KEY)",
			"CREATE TABLE integer_key (id INTEGER PRIMARY KEY)",
			"CREATE TABLE strict_key (code TEXT PRIMARY KEY) STRICT",
			"CREATE TABLE without_rowid_key (code TEXT PRIMARY KEY) WITHOUT ROWID",
			"CREATE TABLE wide (first TEXT, second TEXT, generated TEXT GENERATED ALWAYS AS (first || second) VIRTUAL)",
		} {
			_, err := client.ExecContext(ctx, statement)
			Expect(err).ToNot(HaveOccurred())
		}
		catalog, err := Inspect(ctx, client, "sqlite", Options{CacheKey: "sqlite-key-nullability", Refresh: true})
		Expect(err).ToNot(HaveOccurred())
		columns := map[string][]Column{}
		for _, relation := range catalog.Schemas[0].Relations {
			columns[relation.Name] = relation.Columns
		}
		Expect(columns["rowid_key"][0].Nullable).To(Equal(pointer(true)))
		Expect(columns["integer_key"][0].Nullable).To(Equal(pointer(false)))
		Expect(columns["strict_key"][0].Nullable).To(Equal(pointer(false)))
		Expect(columns["without_rowid_key"][0].Nullable).To(Equal(pointer(false)))
		bounded, err := Inspect(ctx, client, "sqlite", Options{CacheKey: "sqlite-column-limit", Refresh: true, Limits: Limits{MaxColumns: 5}})
		Expect(err).ToNot(HaveOccurred())
		columnCount := 0
		for _, relation := range bounded.Schemas[0].Relations {
			columnCount += len(relation.Columns)
		}
		Expect(columnCount).To(Equal(5))
		Expect(bounded.Truncated).To(BeTrue())
	})

	It("keeps partial-index predicates and resolves implicit composite references", func(ctx SpecContext) {
		client, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "constraints.db"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(client.Close)
		client.SetMaxOpenConns(1)
		for _, statement := range []string{
			"CREATE TABLE parents (tenant TEXT, code TEXT, PRIMARY KEY (tenant, code))",
			"CREATE TABLE children (tenant TEXT, code TEXT, active INTEGER, FOREIGN KEY (tenant, code) REFERENCES parents)",
			"CREATE UNIQUE INDEX active_code ON children(code) WHERE active = 1",
		} {
			_, err := client.ExecContext(ctx, statement)
			Expect(err).ToNot(HaveOccurred())
		}
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		catalog, err := Inspect(bounded, client, "sqlite", Options{CacheKey: "sqlite-constraint-detail", Refresh: true})
		Expect(err).ToNot(HaveOccurred())
		var children Relation
		for _, relation := range catalog.Schemas[0].Relations {
			if relation.Name == "children" {
				children = relation
			}
		}
		Expect(children.Indexes).To(ContainElement(And(HaveField("Name", "active_code"), HaveField("Filter", "active = 1"))))
		Expect(children.Columns[1].Unique).To(BeFalse())
		Expect(children.ForeignKeys).To(ContainElement(And(HaveField("Columns", []string{"tenant", "code"}), HaveField("ReferencedColumns", []string{"tenant", "code"}))))
	})

	It("applies the metadata limit to complete foreign keys", func(ctx SpecContext) {
		client, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "metadata-limit.db"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(client.Close)
		for _, statement := range []string{
			"CREATE TABLE parents (tenant TEXT, code TEXT, PRIMARY KEY (tenant, code))",
			"CREATE TABLE children (tenant TEXT, code TEXT, alt_tenant TEXT, alt_code TEXT, FOREIGN KEY (tenant, code) REFERENCES parents, FOREIGN KEY (alt_tenant, alt_code) REFERENCES parents)",
		} {
			_, err := client.ExecContext(ctx, statement)
			Expect(err).ToNot(HaveOccurred())
		}
		catalog, err := Inspect(ctx, client, "sqlite", Options{CacheKey: "sqlite-metadata-limit", Refresh: true, Limits: Limits{MaxMetadata: 1}})
		Expect(err).ToNot(HaveOccurred())
		Expect(catalog.Truncated).To(BeTrue())
		Expect(catalog.Schemas[0].Relations[0].ForeignKeys).To(HaveLen(1))
		Expect(catalog.Schemas[0].Relations[0].ForeignKeys[0].Columns).To(HaveLen(2))
		Expect(catalog.Schemas[0].Relations[0].ForeignKeys[0].ReferencedColumns).To(Equal([]string{"tenant", "code"}))
	})

	It("keeps metadata available after an oversized view definition", func(ctx SpecContext) {
		client, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "definition-limit.db"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(client.Close)
		for _, statement := range []string{
			"CREATE VIEW a_view AS SELECT 1 AS value",
			"CREATE TABLE z_table (value TEXT)",
			"CREATE INDEX z_value_idx ON z_table(value)",
		} {
			_, err := client.ExecContext(ctx, statement)
			Expect(err).ToNot(HaveOccurred())
		}
		catalog, err := Inspect(ctx, client, "sqlite", Options{CacheKey: "sqlite-definition-limit", Refresh: true, Limits: Limits{MaxMetadata: 1, MaxDefinitionBytes: 5}})
		Expect(err).ToNot(HaveOccurred())
		Expect(catalog.Truncated).To(BeTrue())
		Expect(catalog.Schemas[0].Relations[0].ViewDef).To(BeEmpty())
		Expect(catalog.Schemas[0].Relations[1].Indexes).To(ContainElement(HaveField("Name", "z_value_idx")))
	})
})

func pointer[T any](value T) *T { return &value }

func TestSQLInspection(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Inspection")
}
