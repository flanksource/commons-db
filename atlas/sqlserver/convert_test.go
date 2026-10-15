package sqlserver_test

import (
	"ariga.io/atlas/sql/schema"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/atlas/sqlserver"
	"github.com/flanksource/commons-db/internal/mssqlcatalog"
)

var _ = Describe("SQL Server column types", func() {
	DescribeTable("ParseType reads a declared type and FormatType writes its canonical spelling",
		func(declared, canonical string) {
			parsed, err := sqlserver.ParseType(declared)
			Expect(err).ToNot(HaveOccurred())
			formatted, err := sqlserver.FormatType(parsed)
			Expect(err).ToNot(HaveOccurred())
			Expect(formatted).To(Equal(canonical))

			reparsed, err := sqlserver.ParseType(formatted)
			Expect(err).ToNot(HaveOccurred())
			Expect(reparsed).To(Equal(parsed), "the canonical spelling is a fixed point")
		},
		Entry("bit", "BIT", "bit"),
		Entry("tinyint", "tinyint", "tinyint"),
		Entry("smallint", "smallint", "smallint"),
		Entry("int", "INT", "int"),
		Entry("bigint", "bigint", "bigint"),
		Entry("decimal with precision and scale", "decimal(18, 2)", "decimal(18,2)"),
		Entry("decimal defaults to (18,0)", "decimal", "decimal(18,0)"),
		Entry("numeric keeps its name", "NUMERIC(10)", "numeric(10,0)"),
		Entry("money", "money", "money"),
		Entry("smallmoney", "smallmoney", "smallmoney"),
		Entry("float defaults to 53 bits", "float", "float"),
		Entry("float(53) is float", "float(53)", "float"),
		Entry("float(24) is stored as real", "float(24)", "real"),
		Entry("real", "REAL", "real"),
		Entry("char defaults to length 1", "char", "char(1)"),
		Entry("varchar(max)", "varchar(MAX)", "varchar(max)"),
		Entry("nchar", "nchar(10)", "nchar(10)"),
		Entry("nvarchar", "NVARCHAR(50)", "nvarchar(50)"),
		Entry("nvarchar(max)", "nvarchar(max)", "nvarchar(max)"),
		Entry("text", "text", "text"),
		Entry("ntext stays ntext", "NTEXT", "ntext"),
		Entry("binary", "binary(16)", "binary(16)"),
		Entry("varbinary(max)", "varbinary(max)", "varbinary(max)"),
		Entry("image", "image", "image"),
		Entry("date", "date", "date"),
		Entry("time defaults to 7 digits", "time", "time(7)"),
		Entry("time(3)", "TIME(3)", "time(3)"),
		Entry("datetime", "datetime", "datetime"),
		Entry("datetime2(0)", "datetime2(0)", "datetime2(0)"),
		Entry("smalldatetime", "smalldatetime", "smalldatetime"),
		Entry("datetimeoffset(2)", "datetimeoffset(2)", "datetimeoffset(2)"),
		Entry("uniqueidentifier", "UNIQUEIDENTIFIER", "uniqueidentifier"),
		Entry("xml", "xml", "xml"),
		Entry("sql_variant", "sql_variant", "sql_variant"),
		Entry("rowversion", "rowversion", "rowversion"),
		Entry("timestamp is rowversion", "timestamp", "rowversion"),
	)

	DescribeTable("ParseType refuses types the driver does not manage",
		func(declared, message string) {
			_, err := sqlserver.ParseType(declared)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("an alias type", "sysname", `type "sysname" is not supported`),
		Entry("a spatial type", "geography", `type "geography" is not supported`),
		Entry("a float precision out of range", "float(54)", "float precision 54 is out of range 1-53"),
		Entry("a length on an unsized type", "int(4)", "takes no size"),
		Entry("trailing text", "int identity", "after the type"),
	)

	DescribeTable("the catalog's sys.columns widths map to the declared type",
		func(column mssqlcatalog.Column, canonical string) {
			t, err := sqlserver.CatalogType(column)
			Expect(err).ToNot(HaveOccurred())
			formatted, err := sqlserver.FormatType(t)
			Expect(err).ToNot(HaveOccurred())
			Expect(formatted).To(Equal(canonical))
		},
		Entry("nvarchar bytes are halved", catalogColumn("nvarchar", 100, 0, 0), "nvarchar(50)"),
		Entry("nvarchar(max) is -1", catalogColumn("nvarchar", -1, 0, 0), "nvarchar(max)"),
		Entry("varchar bytes are characters", catalogColumn("varchar", 30, 0, 0), "varchar(30)"),
		Entry("nchar bytes are halved", catalogColumn("nchar", 20, 0, 0), "nchar(10)"),
		Entry("varbinary(max)", catalogColumn("varbinary", -1, 0, 0), "varbinary(max)"),
		Entry("decimal precision and scale", catalogColumn("decimal", 9, 18, 2), "decimal(18,2)"),
		Entry("float(53)", catalogColumn("float", 8, 53, 0), "float"),
		Entry("real", catalogColumn("real", 4, 24, 0), "real"),
		Entry("datetime2 fractional digits", catalogColumn("datetime2", 8, 27, 7), "datetime2(7)"),
		Entry("datetimeoffset fractional digits", catalogColumn("datetimeoffset", 10, 34, 3), "datetimeoffset(3)"),
		Entry("timestamp is rowversion", catalogColumn("timestamp", 8, 0, 0), "rowversion"),
		Entry("int ignores its byte width", catalogColumn("int", 4, 10, 0), "int"),
	)

	It("refuses a catalog column without sys.columns widths", func() {
		_, err := sqlserver.CatalogType(mssqlcatalog.Column{Table: "acme_orders", Name: "note", DataType: "nvarchar"})
		Expect(err).To(MatchError(ContainSubstring(`column "note" has no sys.columns width`)))
	})

	It("refuses to format a type that is not a SQL Server type", func() {
		_, err := sqlserver.FormatType(&schema.JSONType{T: "jsonb"})
		Expect(err).To(MatchError(ContainSubstring("unsupported type *schema.JSONType")))
	})
})

func catalogColumn(dataType string, maxLength, precision, scale int) mssqlcatalog.Column {
	return mssqlcatalog.Column{
		Table: "acme_orders", Name: "value", DataType: dataType,
		MaxLength: &maxLength, Precision: &precision, Scale: &scale,
	}
}
