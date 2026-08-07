import { afterEach, describe, expect, it, vi } from "vitest";
import { makeFieldValueLookup, valueLookupField } from "./esFieldValues";
import type { EsFieldMapping } from "./esQueryOperators";

const fields: EsFieldMapping[] = [
  { name: "@timestamp", dataType: "date", aggregatable: true },
  { name: "service.name", dataType: "keyword", aggregatable: true },
  { name: "message", dataType: "text", aggregatable: false },
  { name: "message.keyword", dataType: "keyword", aggregatable: true },
  { name: "trace.id", dataType: "text", aggregatable: false },
];

describe("valueLookupField", () => {
  it("aggregates a keyword field on itself", () => {
    expect(valueLookupField(fields, "service.name")).toBe("service.name");
  });

  it("aggregates an analyzed text field through its keyword sibling", () => {
    expect(valueLookupField(fields, "message")).toBe("message.keyword");
  });

  it("offers no lookup for a text field without a keyword sibling", () => {
    expect(valueLookupField(fields, "trace.id")).toBeUndefined();
  });

  it("offers no lookup for a date field, whose values are all distinct", () => {
    expect(valueLookupField(fields, "@timestamp")).toBeUndefined();
  });

  it("offers no lookup for a field the mappings do not describe", () => {
    expect(valueLookupField(fields, "unmapped")).toBeUndefined();
    expect(valueLookupField(fields, undefined)).toBeUndefined();
  });
});

const baseUrl = "/api/v1/connection/abc/browser";
const search = { query: { op: "term", field: "env", value: "prod" } };

const respondWith = (body: unknown, status = 200) =>
  ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => JSON.stringify(body),
  }) as Response;

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("makeFieldValueLookup", () => {
  it("asks nothing without a connection or an index", () => {
    expect(makeFieldValueLookup({ baseUrl: "", index: "logs-*" })).toBeUndefined();
    expect(makeFieldValueLookup({ baseUrl, index: "" })).toBeUndefined();
  });

  it("posts the field, the substring and the scope to the browser", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        respondWith({ values: [{ value: "payments", count: 3 }], total: 9, scoped: true }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const source = makeFieldValueLookup({
      baseUrl,
      index: "logs-*",
      roles: { since: "time-from" },
    });
    const result = await source!({ field: "service.name", search }).fetch("pay");

    expect(result).toEqual({
      values: [{ value: "payments", count: 3 }],
      total: 9,
      scoped: true,
    });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(`${baseUrl}/values`);
    expect(JSON.parse(init.body)).toMatchObject({
      index: "logs-*",
      field: "service.name",
      q: "pay",
      search,
      roles: { since: "time-from" },
    });
  });

  // A sibling condition left half-finished cannot compile, and an empty value
  // list would read as "this field holds nothing". The whole index is asked
  // instead, and the answer says the scope was widened.
  it("retries across the whole index when the scope will not compile", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(respondWith("condition has no value", 422))
      .mockResolvedValueOnce(respondWith({ values: [], total: 0, scoped: false }));
    vi.stubGlobal("fetch", fetchMock);

    const source = makeFieldValueLookup({ baseUrl, index: "logs-*" });
    const result = await source!({ field: "service.name", search }).fetch("");

    expect(result.scoped).toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(JSON.parse(fetchMock.mock.calls[1][1].body).search).toBeUndefined();
  });

  it("surfaces a lookup that fails for any other reason", async () => {
    const fetchMock = vi.fn().mockResolvedValue(respondWith("index_not_found", 404));
    vi.stubGlobal("fetch", fetchMock);

    const source = makeFieldValueLookup({ baseUrl, index: "logs-*" });
    await expect(source!({ field: "service.name", search }).fetch("")).rejects.toThrow(
      /index_not_found/,
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  // The scope is compiled server-side, so a sibling condition interpolating
  // {{.params.env}} only narrows the suggestions if the values travel with it.
  it("posts the parameter values the scope is compiled against", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(respondWith({ values: [], total: 0, scoped: true }));
    vi.stubGlobal("fetch", fetchMock);

    const source = makeFieldValueLookup({
      baseUrl,
      index: "logs-*",
      params: { env: "prod" },
    });
    await source!({ field: "service.name", search }).fetch("");

    expect(JSON.parse(fetchMock.mock.calls[0][1].body).params).toEqual({
      env: "prod",
    });
  });

  it("leaves the parameter values off when none are declared", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(respondWith({ values: [], total: 0, scoped: true }));
    vi.stubGlobal("fetch", fetchMock);

    const source = makeFieldValueLookup({ baseUrl, index: "logs-*", params: {} });
    await source!({ field: "service.name", search }).fetch("");

    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).not.toHaveProperty("params");
  });

  it("keys a lookup by every input that changes the compiled value scope", () => {
    const source = makeFieldValueLookup({ baseUrl, index: "logs-*" })!;
    const scoped = source({ field: "service.name", search });
    expect(scoped.key).not.toBe(source({ field: "service.name" }).key);
    expect(scoped.key).not.toBe(source({ field: "host.name", search }).key);
    expect(scoped.key).toBe(source({ field: "service.name", search }).key);

    const parameterized = makeFieldValueLookup({
      baseUrl,
      index: "logs-*",
      params: { env: "prod" },
    })!;
    expect(parameterized({ field: "service.name", search }).key).not.toBe(scoped.key);

    const withRole = makeFieldValueLookup({
      baseUrl,
      index: "logs-*",
      roles: { since: "time-from" },
    })!;
    expect(withRole({ field: "service.name", search }).key).not.toBe(scoped.key);
  });
});
