import { describe, expect, it } from "vitest";
import { logsColumnFilterKeys } from "./logsProfiles";

describe("logsColumnFilterKeys", () => {
  it("maps Clicky table column names to native profile filter parameters", () => {
    expect(
      logsColumnFilterKeys({
        node: {
          kind: "table",
          columns: [
            { name: "level", label: "Level", filterKey: "filter.Level" },
            { name: "message", label: "Message" },
          ],
          rows: [],
        },
      }),
    ).toEqual({ level: "filter.Level" });
  });
});
