import type { OtelSpan } from "@flanksource/clicky-ui/data";

function attributes(row: Record<string, unknown>, key: string): Record<string, unknown> {
  const value = row[key];
  if (value === undefined || value === "") return {};
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${key} must be an object`);
  return value as Record<string, unknown>;
}

export function traceProfileSpans(rows: readonly Record<string, unknown>[]): OtelSpan[] {
  return rows.map((row, index) => {
    for (const key of ["trace_id", "span_id", "timestamp"]) {
      if (typeof row[key] !== "string" || !row[key]) throw new Error(`Row ${index + 1} requires ${key}`);
    }
    if (row.duration_ms === undefined || row.duration_ms === "" || !["string", "number"].includes(typeof row.duration_ms)) throw new Error(`Row ${index + 1} requires duration_ms`);
    const durationMs = Number(row.duration_ms);
    if (!Number.isFinite(durationMs) || durationMs < 0) throw new Error(`Row ${index + 1} has invalid duration_ms`);
    const status = String(row.status ?? "").toLowerCase().replace(/^status_code_/, "");
    const statuses: Record<string, string> = { "": "unset", "0": "unset", "false": "unset", unset: "unset", "1": "ok", ok: "ok", "2": "error", "true": "error", error: "error" };
    if (!Object.hasOwn(statuses, status)) throw new Error(`Row ${index + 1} has unknown status ${status}`);
    const resource: Record<string, unknown> = {};
    const spanAttributes: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(attributes(row, "_attributes"))) {
      if (key.startsWith("resource.")) resource[key.replace(/^resource\.(attributes\.)?/, "")] = value;
      else spanAttributes[key.replace(/^(tag\.|attributes\.)/, "")] = value;
    }
    if (typeof row.service === "string" && row.service !== "") resource["service.name"] = row.service;
    Object.assign(resource, attributes(row, "_resource"));
    Object.assign(spanAttributes, attributes(row, "_span_attributes"));
    const service = row.service === "" || row.service === undefined ? resource["service.name"] ?? "" : row.service;
    if (typeof service !== "string") throw new Error(`Row ${index + 1} has invalid service name`);
    return {
      traceId: row.trace_id as string, spanId: row.span_id as string, startedAt: row.timestamp as string,
      service, operation: String(row.operation ?? ""),
      durationMs, status: statuses[status]!, attributes: spanAttributes, resource,
      ...(row.parent_id ? { parentId: String(row.parent_id) } : {}),
      ...(row.kind ? { kind: String(row.kind) } : {}),
    };
  });
}
