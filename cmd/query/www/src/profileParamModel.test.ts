import { describe, expect, it } from "vitest";
import { paramHasOptions } from "./profileWizardModel";
import { validateProfileParams } from "./profileParamModel";

describe("paramHasOptions", () => {
  it("offers an options picker for the types drawn from a fixed set", () => {
    expect(paramHasOptions({ type: "enum" })).toBe(true);
    expect(paramHasOptions({ type: "list" })).toBe(true);
  });

  it("offers none for a free-text or numeric parameter", () => {
    expect(paramHasOptions({ type: "string" })).toBe(false);
    expect(paramHasOptions({ type: "number" })).toBe(false);
    expect(paramHasOptions({})).toBe(false);
  });
});

describe("validateProfileParams", () => {
  it("accepts a profile with no parameters", () => {
    expect(validateProfileParams([], "opensearch")).toBeNull();
  });

  it("requires a name", () => {
    expect(validateProfileParams([{ label: "Region" }], "sql")).toContain("name");
  });

  it("rejects a duplicated name", () => {
    const error = validateProfileParams([{ name: "region" }, { name: "region" }], "sql");
    expect(error).toContain("region");
  });

  it("rejects a name that squats the column-filter prefix", () => {
    expect(validateProfileParams([{ name: "filter.service" }], "sql")).toContain("filter.");
  });

  // The server drops these keys before params are built, so such a parameter
  // would silently never receive a value.
  it("rejects a name that collides with a reserved request key", () => {
    expect(validateProfileParams([{ name: "format" }], "sql")).toContain("format");
  });

  it("allows a reserved paging key when the parameter claims that role", () => {
    expect(validateProfileParams([{ name: "limit", role: "limit" }], "sql")).toBeNull();
  });

  describe("a bound list parameter", () => {
    it("is rejected on a provider that applies no native filters", () => {
      const error = validateProfileParams(
        [{ name: "regions", type: "list", field: "region" }],
        "sql",
      );
      expect(error).toContain("sql");
    });

    it("is accepted on opensearch", () => {
      expect(
        validateProfileParams([{ name: "regions", type: "list", field: "region" }], "opensearch"),
      ).toBeNull();
    });

    it("is accepted on opentelemetry", () => {
      expect(
        validateProfileParams(
          [{ name: "regions", type: "list", field: "region" }],
          "opentelemetry",
        ),
      ).toBeNull();
    });

    it("is allowed unbound on any provider, since it can hold no exclusion", () => {
      expect(validateProfileParams([{ name: "regions", type: "list" }], "sql")).toBeNull();
    });
  });

  it("rejects a field on a parameter that is not a list", () => {
    const error = validateProfileParams(
      [{ name: "region", type: "enum", field: "region" }],
      "opensearch",
    );
    expect(error).toContain("list");
  });

  it("rejects a list parameter claiming a paging role", () => {
    const error = validateProfileParams(
      [{ name: "rows", type: "list", role: "limit" }],
      "opensearch",
    );
    expect(error).toContain("rows");
  });

  describe("options that cannot survive the wire format", () => {
    it("rejects an option containing a comma", () => {
      const error = validateProfileParams(
        [{ name: "regions", type: "list", options: ["us-east", "eu,west"] }],
        "opensearch",
      );
      expect(error).toContain("eu,west");
    });

    it("rejects an option whose leading ! would read as an exclusion", () => {
      const error = validateProfileParams(
        [{ name: "regions", type: "list", options: ["!eu"] }],
        "opensearch",
      );
      expect(error).toContain("!eu");
    });

    it("allows a comma in an enum option, which is sent whole", () => {
      expect(
        validateProfileParams([{ name: "region", type: "enum", options: ["eu,west"] }], "sql"),
      ).toBeNull();
    });
  });

  it("rejects a default that is not among the declared options", () => {
    const error = validateProfileParams(
      [{ name: "region", type: "enum", options: ["us", "eu"], default: "mars" }],
      "sql",
    );
    expect(error).toContain("mars");
  });

  it("accepts a default drawn from the options", () => {
    expect(
      validateProfileParams(
        [{ name: "region", type: "enum", options: ["us", "eu"], default: "eu" }],
        "sql",
      ),
    ).toBeNull();
  });

  it("checks every value of a list default against the options", () => {
    const error = validateProfileParams(
      [{ name: "regions", type: "list", options: ["us", "eu"], default: ["eu", "mars"] }],
      "sql",
    );
    expect(error).toContain("mars");
  });
});
