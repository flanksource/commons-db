import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ProfileEditorPreview } from "./profileEditorPreview";
import { ProfileFieldGrid } from "./profileFieldGrid";
import { useProfileFieldState } from "./profileFieldState";
import {
  availableProfileFields,
  renameProfileField,
  type ProfileColumn,
} from "./profileWizardModel";

/** What the last sample (or the stored profile at open time) reported. */
const discovered: ProfileColumn[] = [
  { name: "created_at", type: "datetime" },
  { name: "body", type: "string" },
];

function GridProbe({ configured }: { configured: ProfileColumn[] }) {
  const state = useProfileFieldState({
    discovered,
    configured,
    activeName: "created_at",
    onConfiguredChange: () => undefined,
    onActiveNameChange: () => undefined,
  });
  return <ProfileFieldGrid state={state} />;
}

/** The `value` of the input carrying `ariaLabel`, or "" when it has none. */
const valueOf = (html: string, ariaLabel: string): string => {
  const at = html.indexOf(`aria-label="${ariaLabel}"`);
  expect(at, `no element labelled ${ariaLabel}`).toBeGreaterThan(-1);
  const tag = html.slice(html.lastIndexOf("<", at), html.indexOf(">", at));
  return tag.match(/value="([^"]*)"/)?.[1] ?? "";
};

describe("column configuration in the field grid", () => {
  it("leads with quick actions without selection or width controls", () => {
    const html = renderToStaticMarkup(
      <GridProbe
        configured={[
          { name: "created_at", type: "datetime" },
          { name: "body", type: "string", hidden: true },
        ]}
      />,
    );

    expect(html).toContain('aria-label="Hide created_at"');
    expect(html).toContain('aria-label="Show body"');
    expect(html).toContain('aria-label="Delete created_at"');
    expect(html).toContain('aria-label="Rename created_at"');
    expect(html.indexOf(">Actions</th>")).toBeLessThan(
      html.indexOf(">Field</th>"),
    );
    expect(html).not.toContain('aria-label="Include created_at"');
    expect(html).not.toContain('aria-label="Width for created_at"');
    expect(html).not.toContain(">WIDTH</th>");
    expect(html).toContain('<table class="w-fit ');
  });

  it("renders each field as configured, so an edit survives the keystroke that made it", () => {
    const html = renderToStaticMarkup(
      <GridProbe
        configured={[
          { name: "created_at", type: "datetime", label: "Created", width: 40 },
          { name: "body", type: "string" },
        ]}
      />,
    );

    expect({
      label: valueOf(html, "Label for created_at"),
    }).toEqual({ label: "Created" });
  });

  it("strikes deleted fields and mutes hidden fields", () => {
    const html = renderToStaticMarkup(
      <GridProbe
        configured={[
          { name: "created_at", type: "datetime", hidden: true },
        ]}
      />,
    );

    expect(html).toMatch(/data-field-state="hidden"[^>]*text-muted-foreground[^>]*opacity-60/);
    expect(html).toMatch(/data-field-state="deleted"[^>]*line-through[^>]*opacity-60/);
  });

  it("offers the configured field to editors, so a grid edit composes onto inspector edits", () => {
    expect(
      availableProfileFields(discovered, [
        { name: "created_at", type: "datetime", label: "Created", cel: "row.created_at" },
        { name: "computed", type: "string", cel: "row.a + row.b" },
      ]),
    ).toEqual([
      { name: "created_at", type: "datetime", label: "Created", cel: "row.created_at" },
      { name: "body", type: "string" },
      { name: "computed", type: "string", cel: "row.a + row.b" },
    ]);
  });

  it("replaces a discovered field with its renamed output field", () => {
    expect(
      availableProfileFields(discovered, [
        { name: "created", source: "created_at", type: "datetime" },
        { name: "body", type: "string" },
      ]),
    ).toEqual([
      { name: "created", source: "created_at", type: "datetime" },
      { name: "body", type: "string" },
    ]);
  });

  it("records the original provider key when a direct field is renamed", () => {
    expect(renameProfileField({ name: "created_at", type: "datetime" }, "created"))
      .toEqual({ name: "created", source: "created_at", type: "datetime" });
    expect(renameProfileField(
      { name: "created", source: "created_at", type: "datetime" },
      "created_on",
    )).toEqual({ name: "created_on", source: "created_at", type: "datetime" });
    expect(renameProfileField(
      { name: "calculated", cel: "row.a + row.b" },
      "total",
    )).toEqual({ name: "total", cel: "row.a + row.b" });
  });

  it("previews the renamed value before another sample is run", () => {
    const html = renderToStaticMarkup(
      <ProfileEditorPreview
        columns={[{ name: "created", source: "created_at" }]}
        rows={[{ created_at: "2026-08-04" }]}
      />,
    );

    expect(html).toContain(">created</th>");
    expect(html).toContain(">2026-08-04</td>");
    expect(html).not.toContain(">created_at</th>");
  });
});
