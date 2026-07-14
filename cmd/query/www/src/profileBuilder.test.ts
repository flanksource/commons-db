import { describe, expect, it } from "vitest";
import {
  mapTimestampColumn,
  profileBuilderModalClassName,
  profileColumnTypeLabel,
} from "./profileBuilder";

describe("Build Profile workspace layout", () => {
  it("bounds the modal body and delegates scrolling to its panes", () => {
    expect(profileBuilderModalClassName).toContain("h-[calc(100dvh-2rem)]");
    expect(profileBuilderModalClassName).toContain(
      "profile-builder-workspace-dialog",
    );
  });
});

describe("Build Profile timestamp mapping", () => {
  it("marks exactly one sampled column as the timestamp date-range column", () => {
    expect(
      mapTimestampColumn(
        [
          { name: "created_at", type: "string" },
          { name: "updated_at", type: "datetime", kind: "timestamp" },
        ],
        "created_at",
      ),
    ).toEqual([
      { name: "created_at", type: "datetime", kind: "timestamp" },
      { name: "updated_at", type: "datetime" },
    ]);
  });
});

describe("Build Profile structured type labels", () => {
  it("uses readable labels without changing serialized values", () => {
    expect(profileColumnTypeLabel("key_value")).toBe("KeyValue{}");
    expect(profileColumnTypeLabel("key_values")).toBe("[]KeyValue");
    expect(profileColumnTypeLabel("json")).toBe("JSON");
    expect(profileColumnTypeLabel("duration")).toBe("duration");
  });
});
