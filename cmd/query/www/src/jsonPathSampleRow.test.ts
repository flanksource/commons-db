import { afterEach, describe, expect, it, vi } from "vitest";
import { evaluateJsonPath, sampleRequestProfile } from "./jsonPathSampleRow";

const profile = {
  profile: "orders",
  namespace: "default",
  provider: { type: "sql", options: { url: "postgres://localhost/orders" } },
  query: "SELECT payload FROM orders",
  params: [{ name: "since", type: "string" }],
  columns: [{ name: "email", source: "payload", jsonpath: "$.user.email" }],
  aliases: [{ name: "user", cel: "row.payload.user" }],
  ignore: ["payload"],
  render: "logs",
};

describe("sampleRequestProfile", () => {
  it("keeps only the keys the sample endpoint accepts", () => {
    // /profile/sample decodes with DisallowUnknownFields, so anything extra —
    // `render` here — turns the whole request into a 400.
    expect(sampleRequestProfile(profile)).toEqual({
      profile: "orders",
      namespace: "default",
      provider: { type: "sql", options: { url: "postgres://localhost/orders" } },
      query: "SELECT payload FROM orders",
      params: [{ name: "since", type: "string" }],
    });
  });

  it("drops the transforms so the raw provider row is sampled", () => {
    const request = sampleRequestProfile(profile)!;

    expect(request).not.toHaveProperty("columns");
    expect(request).not.toHaveProperty("aliases");
    expect(request).not.toHaveProperty("ignore");
  });

  it("names an unnamed draft so the handler accepts it", () => {
    const request = sampleRequestProfile({ ...profile, profile: "   " })!;

    expect(request.profile).toBe("sample");
  });

  it("declines a profile with no provider to sample", () => {
    expect(sampleRequestProfile({ query: "SELECT 1" })).toBeNull();
    expect(sampleRequestProfile({ provider: { type: "" }, query: "SELECT 1" })).toBeNull();
    expect(sampleRequestProfile(undefined)).toBeNull();
  });
});

describe("evaluateJsonPath", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("sends the expression, its root and the row the caller is already holding", async () => {
    const response = { matches: ["OPEN"], count: 1, filterField: "payload.status" };
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify(response), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    const row = { payload: '{"status":"OPEN"}' };
    await expect(evaluateJsonPath({ jsonpath: "$.status", source: "payload", row })).resolves.toEqual(response);

    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe("/api/v1/profile/sample/jsonpath");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(init?.body as string)).toEqual({ jsonpath: "$.status", source: "payload", row });
  });
});
