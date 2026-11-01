import { describe, expect, it } from "vitest";
import { sqlitePath, sqliteFileURL } from "./sqliteFileURL";

describe("SQLite file URLs", () => {
  it("escapes filename delimiters and preserves connection options", () => {
    const path = "/data/a ?#%.db";
    const url = sqliteFileURL(path, "file:/old.db?mode=ro&cache=shared");
    expect(url).toBe("file:/data/a%20%3F%23%25.db?mode=ro&cache=shared");
    expect(sqlitePath(url)).toBe(path);
  });
});
