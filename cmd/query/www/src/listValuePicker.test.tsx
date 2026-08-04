import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ListValuePicker, summarizeListValueLoad } from "./listValuePicker";
import { parseListValueFile } from "./listValueFile";

describe("summarizeListValueLoad", () => {
  const load = (filename: string, text: string, selector?: string) => {
    const parsed = parseListValueFile(filename, text, selector);
    return summarizeListValueLoad(parsed, parsed.values.length);
  };

  it("reports what was selected", () => {
    expect(load("ids.txt", "A-1\nA-2\n")).toContain("2 values");
  });

  it("names the values it had to drop, because they change the result", () => {
    const summary = load("ids.txt", "A-1\nbad,value\n");
    expect(summary).toContain("1 skipped");
    expect(summary).toContain("comma");
  });

  it("mentions duplicates and blanks rather than silently absorbing them", () => {
    const summary = load("ids.txt", "A-1\nA-1\n\nA-2\n");
    expect(summary).toContain("duplicate");
  });

  it("surfaces a parse error instead of a count", () => {
    expect(load("ids.yaml", "- A-1\n")).toContain("supported");
  });
});

describe("ListValuePicker", () => {
  const render = (props: Partial<Parameters<typeof ListValuePicker>[0]> = {}) =>
    renderToStaticMarkup(
      <ListValuePicker label="Regions" value={{}} onChange={() => {}} {...props} />,
    );

  it("labels the control with the parameter it loads into", () => {
    expect(render()).toContain("Regions");
  });

  it("offers a file input that accepts the formats the parser reads", () => {
    const markup = render();
    expect(markup).toContain(".csv");
    expect(markup).toContain(".json");
    expect(markup).toContain(".txt");
  });

  it("offers both include and exclude, so a file can load either way", () => {
    const markup = render();
    expect(markup).toContain("Include");
    expect(markup).toContain("Exclude");
  });

  it("reports the current selection so a loaded list is visible when collapsed", () => {
    expect(render({ value: { "A-1": "include", "A-2": "exclude" } })).toContain("2 selected");
  });

  it("says nothing is selected rather than showing an empty count", () => {
    expect(render({ value: {} })).toContain("None selected");
  });
});
