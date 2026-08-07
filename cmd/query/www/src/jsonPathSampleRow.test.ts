import { describe, expect, it } from "vitest";
import { sampleRequestProfile } from "./jsonPathSampleRow";

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
