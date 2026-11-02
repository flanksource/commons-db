import { describe, expect, it } from "vitest";
import { traceProfileSpans } from "./traceProfileModel";

const row = { trace_id: "trace-a", span_id: "span-a", parent_id: "", timestamp: "2026-11-02T10:00:00.000100Z", duration_ms: "0.25", service: "checkout", operation: "POST /orders", status: "STATUS_CODE_ERROR", _attributes: { "tag.db.system": "postgresql", "resource.attributes.service.name": "checkout" }, _span_attributes: { "db.rows": 3, cached: false }, _resource: { "deployment.environment": "test" } };

describe("traceProfileSpans", () => {
  it("maps provider rows while preserving timestamp precision and typed attributes", () => {
    expect(traceProfileSpans([row])).toEqual([{ traceId: "trace-a", spanId: "span-a", service: "checkout", operation: "POST /orders", startedAt: row.timestamp, durationMs: 0.25, status: "error", attributes: { "db.system": "postgresql", "db.rows": 3, cached: false }, resource: { "service.name": "checkout", "deployment.environment": "test" } }]);
  });

  it.each(["true", "2", "error"])("normalizes error status %s", status => {
    expect(traceProfileSpans([{ ...row, status }])[0]?.status).toBe("error");
  });

  it("uses resource service identity when the span's service field is empty", () => {
    expect(traceProfileSpans([{ ...row, service: "", _resource: { "service.name": "warehouse" } }])[0]?.service).toBe("warehouse");
  });

  it.each([{}, { ...row, duration_ms: "bad" }, { ...row, status: "unexpected" }, { ...row, status: "constructor" }])("rejects invalid trace rows", invalid => {
    expect(() => traceProfileSpans([invalid])).toThrow();
  });
});
