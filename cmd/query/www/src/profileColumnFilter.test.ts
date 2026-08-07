import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import { describe, expect, it } from "vitest";
import { ProfileFieldEditorForm } from "./profileFieldEditor";
import {
  inferredFilterKind,
  patchColumnFilter,
  patchProfileField,
  PROFILE_FILTER_DEFAULT_LIMIT,
  type ProfileColumn,
} from "./profileWizardModel";

describe("patchColumnFilter", () => {
  it("merges one knob without disturbing the others", () => {
    expect(patchColumnFilter({ kind: "terms", limit: 10 }, { multi: false })).toEqual({
      kind: "terms",
      limit: 10,
      multi: false,
    });
  });

  // The distinction the server reads: an absent block means "infer this", an
  // empty one would mean "override it with nothing".
  it("drops the block once the last knob is cleared", () => {
    expect(patchColumnFilter({ limit: 10 }, { limit: undefined })).toBeUndefined();
  });

  it("drops a block that was never anything", () => {
    expect(patchColumnFilter(undefined, { field: undefined })).toBeUndefined();
  });

  it("creates the block on the first knob set", () => {
    expect(patchColumnFilter(undefined, { limit: 25 })).toEqual({ limit: 25 });
  });

  // false and 0 are values an author chose; only undefined means "unset".
  it("keeps a knob deliberately turned off", () => {
    expect(patchColumnFilter(undefined, { lookup: false })).toEqual({ lookup: false });
  });
});

describe("inferredFilterKind", () => {
  it.each([
    ["number", "range"],
    ["duration", "range"],
    ["bytes", "range"],
    ["datetime", "time"],
    ["boolean", "boolean"],
    ["json", "none"],
    ["key_values", "none"],
    ["string", "terms"],
    ["status", "terms"],
    [undefined, "terms"],
  ])("reads %s as %s, matching the server", (type, expected) => {
    expect(inferredFilterKind({ name: "c", ...(type ? { type } : {}) })).toBe(expected);
  });
});

describe("the filter block survives editing the rest of the column", () => {
  it("is untouched by a label edit", () => {
    const column: ProfileColumn = {
      name: "tenant",
      type: "string",
      filter: { limit: 10, lookup: true },
    };
    expect(patchProfileField(column, { label: "Tenant" }).filter).toEqual({
      limit: 10,
      lookup: true,
    });
  });
});

describe("the column inspector", () => {
  const render = (field: ProfileColumn) =>
    renderToStaticMarkup(createElement(ProfileFieldEditorForm, { field, onChange: () => {} }));

  it("offers the lookup limit for a value selection", () => {
    const markup = render({ name: "tenant", type: "string" });

    expect(markup).toContain("Values offered");
    // Blank means the server's default, so the placeholder has to name it.
    expect(markup).toContain(`placeholder="${PROFILE_FILTER_DEFAULT_LIMIT}"`);
    expect(markup).toContain(`top ${PROFILE_FILTER_DEFAULT_LIMIT}`);
  });

  // A range is typed rather than picked, so a cap on a list it does not have
  // would be a control with nothing behind it.
  it("offers no lookup limit for a range", () => {
    expect(render({ name: "latency_ms", type: "number" })).not.toContain("Values offered");
  });

  it("shows a declared limit in the collapsed summary", () => {
    expect(render({ name: "tenant", type: "string", filter: { limit: 7 } })).toContain("top 7");
  });

  it("reports a filter turned off without claiming a control", () => {
    const markup = render({ name: "tenant", type: "string", filter: { disabled: true } });
    expect(markup).toContain(">off<");
  });

  // Enumerated values are the answer a lookup would fetch, so the two cannot
  // both be on — and a disabled checkbox says so better than a silent override.
  it("disables the lookup toggle once values are listed", () => {
    const markup = render({
      name: "tenant",
      type: "string",
      filter: { options: ["prod", "dev"] },
    });
    expect(markup).toContain("Values are listed above");
    expect(markup).toContain("prod, dev");
  });
});
