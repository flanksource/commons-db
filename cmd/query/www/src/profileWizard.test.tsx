import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { ProfileFieldManager } from "./profileFieldManager";
import {
  applyVisibleFieldSelection,
  filterProfileFields,
  patchProfileField,
  profileWizardSteps,
  type ProfileColumn,
} from "./profileWizardModel";

const discoveredFields: ProfileColumn[] = [
  { name: "@timestamp", type: "datetime" },
  ...Array.from({ length: 125 }, (_, index) => ({
    name: `field_${String(index + 1).padStart(3, "0")}`,
    type: index % 2 === 0 ? "string" : "number",
  })),
];

describe("profile wizard flow", () => {
  it("uses four task-focused steps instead of exposing the raw schema", () => {
    expect(profileWizardSteps).toEqual([
      { id: "source", label: "Choose source", description: "Connection" },
      { id: "query", label: "Explore & sample", description: "Query" },
      { id: "fields", label: "Name & shape", description: "Fields" },
      { id: "review", label: "Review", description: "Save" },
    ]);
  });
});

describe("large profile field sets", () => {
  it("filters every discovered field by search, type, and selection state", () => {
    const selectedNames = new Set(
      discoveredFields.slice(0, 48).map((field) => field.name),
    );

    expect(
      filterProfileFields(discoveredFields, selectedNames, {
        query: "field_12",
        type: "number",
        selection: "unselected",
      }).map((field) => field.name),
    ).toEqual(["field_120", "field_122", "field_124"]);
  });

  it("bulk-selects only visible fields while preserving configured metadata", () => {
    const configured = [
      {
        name: "@timestamp",
        type: "datetime",
        kind: "timestamp",
        label: "Observed at",
      },
    ];
    const next = applyVisibleFieldSelection(
      discoveredFields,
      configured,
      new Set(["@timestamp", "field_002"]),
      true,
    );

    expect(next).toEqual([
      {
        name: "@timestamp",
        type: "datetime",
        kind: "timestamp",
        label: "Observed at",
      },
      { name: "field_002", type: "number" },
    ]);
  });

  it("patches an edited field without dropping opaque schema properties", () => {
    expect(
      patchProfileField(
        {
          name: "duration_ms",
          type: "number",
          format: "0,0",
          vendor: { source: "sample" },
        },
        { label: "Duration", width: 140, hidden: true },
      ),
    ).toEqual({
      name: "duration_ms",
      type: "number",
      label: "Duration",
      format: "0,0",
      width: 140,
      hidden: true,
      vendor: { source: "sample" },
    });
  });

  it("renders the full selection summary and the active field editor", () => {
    const html = renderToStaticMarkup(
      <ProfileFieldManager
        discovered={discoveredFields}
        configured={discoveredFields.slice(0, 48)}
        activeName="@timestamp"
        onConfiguredChange={vi.fn()}
        onActiveNameChange={vi.fn()}
      />,
    );

    expect(html).toContain("48 of 126 fields selected");
    expect(html).toContain("Search 126 fields");
    expect(html).toContain("Field editor");
    expect(html).toContain("Display label");
    expect(html).toContain("CEL expression");
    expect(html).toContain("@timestamp");
  });
});
