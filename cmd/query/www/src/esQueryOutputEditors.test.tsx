import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { EsQuerySortEditor, moveSortEntry } from "./esQuerySortEditor";
import {
  EsQueryOutputEditor,
  parseCount,
  pruneEmpty,
} from "./esQueryOutputEditor";
import type { EsSearch, EsSortBy } from "./esQueryBuilderModel";
import type { EsFieldMapping } from "./esQueryOperators";

const fields: EsFieldMapping[] = [
  { name: "@timestamp", dataType: "date", searchable: true, aggregatable: true },
  { name: "message", dataType: "text", searchable: true, aggregatable: false },
  { name: "level", dataType: "keyword", searchable: true, aggregatable: true },
];

const renderSort = (sort: EsSortBy[]) =>
  renderToStaticMarkup(
    <EsQuerySortEditor
      sort={sort}
      fields={fields}
      orders={["asc", "desc"]}
      onChange={() => undefined}
    />,
  );

/** The opening tag of the element carrying `ariaLabel`, attributes and all. */
const openingTag = (html: string, ariaLabel: string): string => {
  const at = html.indexOf(`aria-label="${ariaLabel}"`);
  expect(at, `no element labelled ${ariaLabel}`).toBeGreaterThan(-1);
  return html.slice(at, html.indexOf(">", at));
};

const renderOutput = (search: EsSearch) =>
  renderToStaticMarkup(
    <EsQueryOutputEditor search={search} onChange={() => undefined} />,
  );

describe("reordering sort entries", () => {
  const sort: EsSortBy[] = [
    { field: "@timestamp" },
    { field: "level" },
    { field: "_score" },
  ];

  it("swaps an entry with the one before it", () => {
    expect(moveSortEntry(sort, 1, -1).map((entry) => entry.field)).toEqual([
      "level",
      "@timestamp",
      "_score",
    ]);
  });

  it("swaps an entry with the one after it", () => {
    expect(moveSortEntry(sort, 0, 1).map((entry) => entry.field)).toEqual([
      "level",
      "@timestamp",
      "_score",
    ]);
  });

  // Wrapping would silently make the first tie-break the last, which is the
  // opposite of what a click on a disabled-looking arrow should do.
  it("clamps at both ends rather than wrapping", () => {
    expect(moveSortEntry(sort, 0, -1)).toBe(sort);
    expect(moveSortEntry(sort, 2, 1)).toBe(sort);
  });

  it("leaves the entries it did not move alone", () => {
    expect(moveSortEntry(sort, 1, -1)[2]).toBe(sort[2]);
  });
});

describe("the sort editor", () => {
  it("renders one field and order control per sort entry", () => {
    const html = renderSort([
      { field: "@timestamp", order: "desc" },
      { field: "level" },
    ]);
    expect(html.match(/aria-label="Sort field"/g)).toHaveLength(2);
    expect(html).toContain('value="@timestamp"');
    expect(html).toContain('<option value="desc" selected="">desc</option>');
  });

  it("says plainly when nothing sorts the hits", () => {
    expect(renderSort([])).toContain("Unsorted");
    expect(renderSort([{ field: "level" }])).not.toContain("Unsorted");
  });

  it("names each move control after the field it moves", () => {
    const html = renderSort([{ field: "@timestamp" }, { field: "level" }]);
    expect(html).toContain('aria-label="Move @timestamp later"');
    expect(html).toContain('aria-label="Move level earlier"');
    expect(html).toContain('aria-label="Remove level"');
  });

  it("disables the move that would run off either end", () => {
    const html = renderSort([{ field: "@timestamp" }, { field: "level" }]);
    // The bare word also appears in Tailwind's disabled: variants, so this has
    // to look for the attribute itself.
    expect(openingTag(html, "Move @timestamp earlier")).toContain('disabled=""');
    expect(openingTag(html, "Move level later")).toContain('disabled=""');
    expect(openingTag(html, "Move @timestamp later")).not.toContain('disabled=""');
    expect(openingTag(html, "Move level earlier")).not.toContain('disabled=""');
  });
});

describe("pruning an empty sub-object", () => {
  it("keeps a sub-object that still says something", () => {
    expect(pruneEmpty({ enabled: false })).toEqual({ enabled: false });
    expect(pruneEmpty({ includes: ["user.*"] })).toEqual({
      includes: ["user.*"],
    });
  });

  // Storing `{}` would leave a key the compiler has to ignore, so clearing the
  // last field has to clear the object with it.
  it("drops one whose every field was cleared", () => {
    expect(pruneEmpty({ enabled: undefined, includes: [] })).toBeUndefined();
    expect(pruneEmpty({})).toBeUndefined();
  });
});

describe("reading a count", () => {
  it.each([
    ["25", 25],
    ["0", 0],
  ])("accepts %s as %i", (raw, expected) => {
    expect(parseCount(raw)).toBe(expected);
  });

  it.each(["", "  ", "-1", "1.5", "many"])("treats %j as unset", (raw) => {
    expect(parseCount(raw)).toBeUndefined();
  });
});

describe("the output editor", () => {
  it("shows the hit window and the _source controls", () => {
    const html = renderOutput({ size: 100, from: 20 });
    expect(html).toContain('aria-label="Size"');
    expect(html).toContain('value="100"');
    expect(html).toContain('aria-label="From"');
    expect(html).toContain('value="20"');
    expect(html).toContain('aria-label="Add includes pattern"');
    expect(html).toContain('aria-label="Add excludes pattern"');
  });

  it("renders each stored _source pattern as a removable chip", () => {
    const html = renderOutput({ source: { includes: ["user.*", "@timestamp"] } });
    expect(html.match(/es-pattern-chip/g)).toHaveLength(2);
    expect(html).toContain('aria-label="Remove user.*"');
  });

  // With _source off there is nothing to include or exclude, so the pattern
  // lists would only collect values the backend never reads.
  it("hides the pattern lists once _source is turned off", () => {
    const html = renderOutput({ source: { enabled: false } });
    expect(html).not.toContain('aria-label="Add includes pattern"');
  });

  it("asks for a threshold only while total hits are tracked", () => {
    expect(renderOutput({})).not.toContain('aria-label="Total hits threshold"');
    expect(renderOutput({ trackTotalHits: { enabled: true } })).toContain(
      'aria-label="Total hits threshold"',
    );
  });
});
