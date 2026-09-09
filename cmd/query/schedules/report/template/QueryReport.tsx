import {
  Document,
  Page,
  Header,
  Footer,
  PageNo,
  Section,
  DynamicTable,
  CalloutBox,
  type ColumnDef,
  type RowData,
} from "@flanksource/facet";

// ReportData mirrors report.Payload in Go. Rows are keyed objects rather than
// positional arrays so a column reorder in the profile cannot silently shift
// every value one cell to the left.
export interface ReportData {
  name: string;
  title: string;
  subtitle?: string;
  generatedAt: string;
  rowCount: number;
  truncated?: boolean;
  columns: ColumnDef[];
  rows: RowData[];
}

export default function QueryReport({ data }: { data: ReportData }) {
  return (
    <Document
      title={data.title}
      pageSize="a4-landscape"
      margins={{ top: 12, right: 12, bottom: 12, left: 12 }}
    >
      <Header height={10}>{data.title}</Header>
      <Footer height={10}>
        Generated {data.generatedAt} — page <PageNo />
      </Footer>

      <Page title={data.title}>
        <Section title={data.subtitle ?? `${data.rowCount} rows`}>
          {/* A partial result that does not say so is the failure this whole
              pipeline exists to avoid, so it is called out above the table
              rather than left for someone to infer from the row count. */}
          {data.truncated && (
            <CalloutBox>
              This report is incomplete: the query stopped at {data.rowCount} rows.
            </CalloutBox>
          )}
          <DynamicTable columns={data.columns} rows={data.rows} size="xs" />
        </Section>
      </Page>
    </Document>
  );
}
