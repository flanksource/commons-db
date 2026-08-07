import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { applyRowLimit, QueryRowLimits } from "./queryRowLimits";

const defaults = { pageSize: 100, maxPageSize: 1000, maxExportRows: 100000 };

describe("applyRowLimit", () => {
  it("keeps the caps the author left alone", () => {
    expect(
      applyRowLimit({ pageSize: 25, maxExportRows: 5000 }, "maxExportRows", "250000"),
    ).toEqual({ pageSize: 25, maxExportRows: 250000 });
  });

  it("drops a cleared cap so the profile falls back to the default", () => {
    expect(
      applyRowLimit({ pageSize: 25, maxExportRows: 5000 }, "maxExportRows", ""),
    ).toEqual({ pageSize: 25 });
  });

  it("starts a limits block for a profile that had none", () => {
    expect(applyRowLimit(undefined, "pageSize", "200")).toEqual({ pageSize: 200 });
  });

  it("leaves no block once the last cap is cleared", () => {
    expect(applyRowLimit({ maxExportRows: 250000 }, "maxExportRows", "")).toBeUndefined();
  });
});

describe("QueryRowLimits", () => {
  const render = (props: Partial<Parameters<typeof QueryRowLimits>[0]> = {}) =>
    renderToStaticMarkup(
      <QueryRowLimits
        value="5000"
        onChange={() => {}}
        defaults={defaults}
        limits={{ maxExportRows: 250000 }}
        onLimitsChange={() => {}}
        {...props}
      />,
    );

  it("gives each cap its own labelled field", () => {
    const html = render();
    for (const label of ["Limit", "Page size", "Max page", "Max export"]) {
      expect(html).toContain(`aria-label="${label}"`);
    }
  });

  it("holds the query's limit and the caps the profile set", () => {
    const html = render();
    expect(html).toContain('value="5000"');
    expect(html).toContain('value="250000"');
  });

  it("offers the inherited default as the placeholder of an unset cap", () => {
    const html = render();
    expect(html).toContain('placeholder="100"');
    expect(html).toContain('placeholder="1,000"');
  });

  it("shows only the query's own limit to a host that edits no profile", () => {
    const html = render({ onLimitsChange: undefined });
    expect(html).toContain('aria-label="Limit"');
    expect(html).not.toContain('aria-label="Max export"');
  });
});
