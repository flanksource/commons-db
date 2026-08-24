/* Fixture for the modal design playground — mirrors the real `os2` trace
 * profile at /profile-os2 so the variants are judged against realistic field
 * counts, name lengths and value shapes rather than lorem ipsum. */

export type MockField = {
  name: string;
  type: string;
  included: boolean;
  label?: string;
  format?: string;
  unit?: string;
  width?: number;
  cel?: string;
  hidden?: boolean;
  role?: string;
};

export const mockFields: MockField[] = [
  { name: "duration", type: "duration", included: true, label: "Took", unit: "ms" },
  { name: "flags", type: "string", included: true },
  { name: "message", type: "string", included: true, width: 80 },
  { name: "operationName", type: "string", included: true, label: "Operation" },
  { name: "process.serviceName", type: "string", included: true, label: "Service" },
  { name: "process.tags", type: "json", included: true },
  { name: "tags", type: "json", included: true, role: "tags" },
  { name: "timestamp", type: "datetime", included: true, role: "timestamp" },
  { name: "client.address", type: "string", included: true, label: "Client" },
  { name: "http.request.method", type: "string", included: true, label: "Method", width: 8 },
  { name: "http.response.status_code", type: "number", included: true, label: "Status", role: "status" },
  { name: "server.address", type: "string", included: true, label: "Server" },
  {
    name: "url.path",
    type: "string",
    included: true,
    label: "Path",
    cel: "'tags' in row ? jsonpath(\"$[?(@.key == 'url.path')].value\", type(row['tags']) == string ? row['tags'].JSONArray() : row['tags']) : ''",
  },
  { name: "host.name", type: "string", included: true, label: "Host" },
  { name: "user_agent.original", type: "string", included: false, label: "User agent", hidden: true },
  { name: "span.kind", type: "string", included: false },
  { name: "trace.id", type: "string", included: false, width: 32 },
  { name: "parent.span.id", type: "string", included: false },
];

export const fieldTypes = [
  "string",
  "number",
  "boolean",
  "datetime",
  "duration",
  "bytes",
  "status",
  "health",
  "key_value",
  "key_values",
  "json",
];

export const fieldRoles = [
  { value: "", label: "Standard field" },
  { value: "timestamp", label: "Timestamp" },
  { value: "tags", label: "Tags" },
  { value: "status", label: "Status" },
];

export const fieldFormats = [
  { value: "", label: "From Type" },
  { value: "relative", label: "Relative time" },
  { value: "absolute", label: "Absolute time" },
  { value: "decimal", label: "Decimal" },
  { value: "percent", label: "Percent" },
];

export const fieldUnits = [
  { value: "", label: "No unit" },
  { value: "ms", label: "Milliseconds" },
  { value: "s", label: "Seconds" },
  { value: "bytes", label: "Bytes" },
];

export type SectionKey =
  | "general"
  | "source"
  | "columns"
  | "parameters"
  | "advanced"
  | "raw";

export type SectionSpec = {
  key: SectionKey;
  label: string;
  hint: string;
  /** Rendered as a right-aligned count/state chip in the nav variants. */
  badge?: string;
  /** Drives the amber "needs attention" dot. */
  attention?: boolean;
};

export const sections: SectionSpec[] = [
  { key: "general", label: "General", hint: "Name, namespace, render mode", badge: "ok" },
  { key: "source", label: "Source & Query", hint: "Provider, connection, query", badge: "stale", attention: true },
  { key: "columns", label: "Columns", hint: "Field selection and formatting", badge: "14/18" },
  { key: "parameters", label: "Parameters", hint: "Named query inputs", badge: "3" },
  { key: "advanced", label: "Advanced", hint: "Imports, aliases, output", badge: "2" },
  { key: "raw", label: "Raw YAML", hint: "Edit the profile document directly" },
];

/** Rows for the live-preview panes in variants C and D. */
export const previewRows = [
  {
    timestamp: "14:02:11.418",
    "process.serviceName": "checkout-api",
    operationName: "POST /orders",
    "http.response.status_code": 201,
    duration: "182ms",
    "url.path": "/orders",
  },
  {
    timestamp: "14:02:11.402",
    "process.serviceName": "checkout-api",
    operationName: "GET /cart",
    "http.response.status_code": 200,
    duration: "34ms",
    "url.path": "/cart",
  },
  {
    timestamp: "14:02:10.977",
    "process.serviceName": "payments",
    operationName: "POST /charge",
    "http.response.status_code": 502,
    duration: "3.1s",
    "url.path": "/charge",
  },
  {
    timestamp: "14:02:10.844",
    "process.serviceName": "inventory",
    operationName: "GET /stock",
    "http.response.status_code": 200,
    duration: "12ms",
    "url.path": "/stock",
  },
  {
    timestamp: "14:02:10.501",
    "process.serviceName": "checkout-api",
    operationName: "GET /cart",
    "http.response.status_code": 200,
    duration: "28ms",
    "url.path": "/cart",
  },
];

export const previewColumns = [
  "timestamp",
  "process.serviceName",
  "operationName",
  "http.response.status_code",
  "duration",
  "url.path",
];
