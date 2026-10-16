package sqlinspect

import (
	"database/sql"
	"path/filepath"

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
		catalog, err := Inspect(ctx, client, "sqlite", Limits{}, Options{CacheKey: "sqlite-schema-metadata", Refresh: true})
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
		catalog, err := Inspect(ctx, client, "sqlite", Limits{MaxColumns: 2}, Options{CacheKey: "sqlite-independent-limits", Refresh: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(catalog.Truncated).To(BeFalse())
		Expect(catalog.Schemas[0].Relations[0].Indexes).To(HaveLen(1))
	})
})

func pointer[T any](value T) *T { return &value }
