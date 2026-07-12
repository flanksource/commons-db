import { describe, expect, it } from "vitest";
import type { JsonSchemaObject } from "@flanksource/clicky-ui/components";
import { normalizeSchemaForMonaco } from "@flanksource/clicky-ui/monaco/schema";
import connectionSchema from "../../../../schemas/connection.json";

describe("connection schema Monaco compatibility", () => {
  it("normalizes every provider definition and reference", () => {
    const result = normalizeSchemaForMonaco(connectionSchema as unknown as JsonSchemaObject);
    expect(result.unsupportedKeywords).toEqual([]);
    expect(result.schema).toBeDefined();

    const definitions = result.schema?.definitions as Record<string, unknown>;
    expect(Object.keys(definitions)).toHaveLength(55);

    const serialized = JSON.stringify(result.schema);
    expect(serialized).not.toContain("#/$defs/");
    expect(serialized.match(/#\/definitions\//g)).toHaveLength(55);
  });
});
