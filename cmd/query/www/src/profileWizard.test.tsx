import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { ProfileFieldManager } from "./profileFieldManager";
import {
  applyVisibleFieldSelection,
  filterProfileFields,
  patchProfileField,
  PROFILE_COLUMN_FORMAT_OPTIONS,
  PROFILE_COLUMN_UNIT_OPTIONS,
  profileWizardStepReady,
  profileWizardSteps,
  withProfileLimits,
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

describe("writing the row caps onto a draft", () => {
  const draft = { profile: "os", limits: { maxExportRows: 250000 } };

  it("stores the caps the profile sets for itself", () => {
    expect(withProfileLimits(draft, { pageSize: 200 })).toEqual({
      profile: "os",
      limits: { pageSize: 200 },
    });
  });

  it("leaves no block on a profile that caps nothing", () => {
    expect(withProfileLimits(draft, undefined)).toEqual({ profile: "os" });
    expect(withProfileLimits(draft, undefined)).not.toHaveProperty("limits");
  });
});

describe("advancing past the query step", () => {
  const sampled: ProfileColumn[] = [{ name: "@timestamp", type: "datetime" }];

  it("accepts a raw query once fields have been sampled", () => {
    expect(
      profileWizardStepReady("query", { query: "select 1" }, sampled),
    ).toBe(true);
  });

  it("accepts a structured search, which stores no raw query at all", () => {
    expect(
      profileWizardStepReady(
        "query",
        { query: "", provider: { options: { search: { query: { op: "bool" } } } } },
        sampled,
      ),
    ).toBe(true);
  });

  it("blocks a draft that says neither", () => {
    expect(
      profileWizardStepReady("query", { query: "  ", provider: {} }, sampled),
    ).toBe(false);
  });

  it("blocks a query that has never been sampled", () => {
    expect(profileWizardStepReady("query", { query: "select 1" }, [])).toBe(
      false,
    );
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
          format: "float",
          vendor: { source: "sample" },
        },
        { label: "Duration", width: 140, hidden: true },
      ),
    ).toEqual({
      name: "duration_ms",
      type: "number",
      label: "Duration",
      format: "float",
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

  it("uses canonical Format and Unit dropdowns with explanatory help", () => {
    const html = renderToStaticMarkup(
      <ProfileFieldManager
        discovered={discoveredFields}
        configured={discoveredFields.slice(0, 1)}
        activeName="@timestamp"
        onConfiguredChange={vi.fn()}
        onActiveNameChange={vi.fn()}
      />,
    );
    for (const option of [
      ...PROFILE_COLUMN_FORMAT_OPTIONS,
      ...PROFILE_COLUMN_UNIT_OPTIONS,
    ]) {
      expect(html).toContain(`value="${option.value}"`);
    }
    expect(html).toContain("independent of Type");
    expect(html).toContain("Max width (characters)");
  });
});
