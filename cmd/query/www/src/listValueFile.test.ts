import { describe, expect, it } from "vitest";
import { detectListValueColumns, parseListValueFile } from "./listValueFile";

describe("parseListValueFile", () => {
  describe("CSV", () => {
    const accounts = "account_id,region\nA-1,us-east\nA-2,eu\n";

    it("reads the first column when no selector is given", () => {
      expect(parseListValueFile("accounts.csv", accounts).values).toEqual(["A-1", "A-2"]);
    });

    it("reads a named column, matching the header case-insensitively", () => {
      expect(parseListValueFile("accounts.csv", accounts, "Region").values).toEqual([
        "us-east",
        "eu",
      ]);
    });

    it("keeps a quoted field that contains the separator", () => {
      expect(parseListValueFile("a.csv", 'name\n"Acme, Inc"\nBeta\n').rejected).toEqual([
        "Acme, Inc",
      ]);
    });

    it("reads a quoted field containing a newline as one value", () => {
      expect(parseListValueFile("a.csv", 'name\n"two\nlines"\nBeta\n').values).toEqual([
        "two\nlines",
        "Beta",
      ]);
    });

    it("unescapes a doubled quote", () => {
      expect(parseListValueFile("a.csv", 'name\n"say ""hi"""\n').values).toEqual([`say "hi"`]);
    });

    it("handles CRLF line endings", () => {
      expect(parseListValueFile("a.csv", "id\r\nA-1\r\nA-2\r\n").values).toEqual(["A-1", "A-2"]);
    });

    it("reports a selector that matches no header, naming what is available", () => {
      const result = parseListValueFile("accounts.csv", accounts, "missing");
      expect(result.error).toContain("missing");
      expect(result.error).toContain("account_id");
      expect(result.error).toContain("region");
    });
  });

  describe("JSON", () => {
    it("reads a flat string array", () => {
      expect(parseListValueFile("ids.json", '["A-1","A-2"]').values).toEqual(["A-1", "A-2"]);
    });

    it("reads a named key from an array of objects", () => {
      expect(
        parseListValueFile("ids.json", '[{"id":"A-1","r":"eu"},{"id":"A-2","r":"us"}]', "id").values,
      ).toEqual(["A-1", "A-2"]);
    });

    it("infers the only key when every object has exactly one", () => {
      expect(parseListValueFile("ids.json", '[{"id":"A-1"},{"id":"A-2"}]').values).toEqual([
        "A-1",
        "A-2",
      ]);
    });

    it("asks for a key when the objects have several", () => {
      const result = parseListValueFile("ids.json", '[{"id":"A-1","region":"eu"}]');
      expect(result.error).toContain("id");
      expect(result.error).toContain("region");
    });

    it("reports a top-level object, naming what was found", () => {
      expect(parseListValueFile("ids.json", '{"id":"A-1"}').error).toContain("an object");
    });

    it("reports malformed JSON rather than throwing", () => {
      expect(parseListValueFile("ids.json", "[oops").error).toBeTruthy();
    });
  });

  describe("TXT", () => {
    it("reads one value per line", () => {
      expect(parseListValueFile("ids.txt", "A-1\r\nA-2\n\nA-3\n").values).toEqual([
        "A-1",
        "A-2",
        "A-3",
      ]);
    });

    it("keeps a value that begins with #, which is data and not a comment", () => {
      expect(parseListValueFile("ids.txt", "#A-1\nA-2\n").values).toEqual(["#A-1", "A-2"]);
    });
  });

  describe("cleaning", () => {
    it("trims, drops blanks and dedupes while preserving first-seen order", () => {
      const result = parseListValueFile("ids.txt", "  B \nA\nB\n  A  \n\nC\n");
      expect(result.values).toEqual(["B", "A", "C"]);
      expect(result.skippedBlank).toBe(1);
      expect(result.skippedDuplicate).toBe(2);
    });

    it("preserves a leading ! so a file can carry exclusions, as the CLI does", () => {
      expect(parseListValueFile("ids.txt", "A-1\n!A-2\n").values).toEqual(["A-1", "!A-2"]);
    });

    it("rejects a value containing a comma, which the wire format cannot escape", () => {
      const result = parseListValueFile("ids.txt", "A-1\nbad,value\nA-2\n");
      expect(result.values).toEqual(["A-1", "A-2"]);
      expect(result.rejected).toEqual(["bad,value"]);
    });

    it("reports an empty file rather than returning nothing silently", () => {
      expect(parseListValueFile("ids.txt", "\n  \n").error).toContain("no values");
    });
  });

  it("reports an unsupported extension, listing the ones it reads", () => {
    const result = parseListValueFile("ids.yaml", "- A-1\n");
    expect(result.error).toContain(".csv");
    expect(result.error).toContain(".json");
    expect(result.error).toContain(".txt");
  });
});

describe("detectListValueColumns", () => {
  it("lists CSV headers so the picker can offer them", () => {
    expect(detectListValueColumns("a.csv", "account_id,region\nA-1,eu\n")).toEqual([
      "account_id",
      "region",
    ]);
  });

  it("lists the keys of JSON objects", () => {
    expect(detectListValueColumns("a.json", '[{"id":"A-1","region":"eu"}]')).toEqual([
      "id",
      "region",
    ]);
  });

  it("offers nothing to choose for a flat array or a text file", () => {
    expect(detectListValueColumns("a.json", '["A-1"]')).toEqual([]);
    expect(detectListValueColumns("a.txt", "A-1\n")).toEqual([]);
  });
});
