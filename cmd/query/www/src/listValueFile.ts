/**
 * Reads a list parameter's values out of a CSV, JSON or TXT file the user picked
 * in the browser. The rules mirror `cmd/query/internal/paramfile` exactly, so a
 * file behaves the same whether it is passed to `--param ids=@ids.csv#id` or
 * dropped onto the filter — with one deliberate difference: the browser hands
 * over a File and never a path, so there is no `#selector` to type. The column
 * is chosen from `detectListValueColumns` instead.
 *
 * A leading `!` is preserved rather than stripped: it marks an exclusion on the
 * wire, and a file may carry them just as a typed selection may. A value holding
 * a comma is rejected instead, because the wire format separates on commas and
 * has no escape — silently keeping it would filter on something the user never
 * asked for.
 */

export type ListValueParse = {
  values: string[];
  /** Header/key names the file offers, for the column picker. */
  columns: string[];
  skippedBlank: number;
  skippedDuplicate: number;
  /** Values dropped because they cannot survive the wire format. */
  rejected: string[];
  /** Set when nothing could be read; `values` is then empty. */
  error?: string;
};

const SUPPORTED = ".csv, .json or .txt";

function extensionOf(filename: string): string {
  const dot = filename.lastIndexOf(".");
  return dot < 0 ? "" : filename.slice(dot).toLowerCase();
}

/** read is what one format reader returns: an error still carries the columns,
 * so the picker can offer a choice for the very error that asks for one. */
type read = { values: string[]; columns: string[]; error?: string };

export function parseListValueFile(
  filename: string,
  text: string,
  selector?: string,
): ListValueParse {
  let result: read;
  switch (extensionOf(filename)) {
    case ".csv":
      result = readCSV(text, selector);
      break;
    case ".json":
      result = readJSON(text, selector);
      break;
    case ".txt":
      result = readLines(text);
      break;
    default:
      result = {
        values: [],
        columns: [],
        error: `${filename} is not a supported file; expected ${SUPPORTED}`,
      };
  }

  const cleaned = clean(result.values);
  if (result.error) return { ...cleaned, columns: result.columns, error: result.error };
  if (cleaned.values.length === 0 && cleaned.rejected.length === 0) {
    return { ...cleaned, columns: result.columns, error: `${filename} contained no values` };
  }
  return { ...cleaned, columns: result.columns };
}

/** detectListValueColumns lists the names a file offers to select a column by. */
export function detectListValueColumns(filename: string, text: string): string[] {
  const parsed = parseListValueFile(filename, text);
  return parsed.columns;
}

function clean(values: string[]): Omit<ListValueParse, "columns" | "error"> {
  const seen = new Set<string>();
  const out: string[] = [];
  const rejected: string[] = [];
  let skippedBlank = 0;
  let skippedDuplicate = 0;

  for (const raw of values) {
    const value = raw.trim();
    if (value === "") {
      skippedBlank++;
      continue;
    }
    if (value.includes(",")) {
      rejected.push(value);
      continue;
    }
    if (seen.has(value)) {
      skippedDuplicate++;
      continue;
    }
    seen.add(value);
    out.push(value);
  }
  return { values: out, skippedBlank, skippedDuplicate, rejected };
}

/**
 * readCSV parses the RFC4180 subset real exports use: double-quoted fields,
 * doubled quotes as an escape, and separators or newlines inside quotes. The
 * first record is always the header, so a named column can resolve — a
 * headerless file should be read as .txt.
 */
function readCSV(text: string, selector?: string): read {
  const records = splitCSVRecords(text);
  if (records.length === 0) return { values: [], columns: [] };

  const header = records[0].map((name) => name.trim());
  let index = 0;
  if (selector) {
    index = header.findIndex((name) => name.toLowerCase() === selector.toLowerCase());
    if (index < 0) {
      return {
        values: [],
        columns: header,
        error: `column "${selector}" not found (have: ${header.join(", ")})`,
      };
    }
  }
  return { values: records.slice(1).map((record) => record[index] ?? ""), columns: header };
}

function splitCSVRecords(text: string): string[][] {
  const records: string[][] = [];
  let record: string[] = [];
  let field = "";
  let quoted = false;
  let dirty = false;

  const endField = () => {
    record.push(field);
    field = "";
    dirty = true;
  };
  const endRecord = () => {
    endField();
    records.push(record);
    record = [];
    dirty = false;
  };

  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    if (quoted) {
      if (char !== '"') {
        field += char;
      } else if (text[i + 1] === '"') {
        field += '"';
        i++;
      } else {
        quoted = false;
      }
      continue;
    }
    switch (char) {
      case '"':
        quoted = true;
        dirty = true;
        break;
      case ",":
        endField();
        break;
      case "\r":
        break;
      case "\n":
        endRecord();
        break;
      default:
        field += char;
        dirty = true;
    }
  }
  if (dirty || field !== "") endRecord();
  return records;
}

function readJSON(text: string, selector?: string): read {
  let decoded: unknown;
  try {
    decoded = JSON.parse(text);
  } catch (error) {
    return { values: [], columns: [], error: `could not parse JSON: ${(error as Error).message}` };
  }
  if (!Array.isArray(decoded)) {
    return { values: [], columns: [], error: `expected a JSON array, found ${jsonKind(decoded)}` };
  }

  const columns = objectKeys(decoded);
  const key = selector ?? (columns.length === 1 ? columns[0] : undefined);
  const fail = (error: string): read => ({ values: [], columns, error });
  const values: string[] = [];

  for (let i = 0; i < decoded.length; i++) {
    const item = decoded[i];
    if (typeof item === "string") {
      values.push(item);
      continue;
    }
    if (item !== null && typeof item === "object" && !Array.isArray(item)) {
      if (!key) {
        return fail(`item ${i} is an object with several keys; choose one of: ${columns.join(", ")}`);
      }
      const raw = (item as Record<string, unknown>)[key];
      if (raw === undefined) return fail(`item ${i} has no key "${key}"`);
      if (typeof raw !== "string") {
        return fail(`item ${i} key "${key}" is ${jsonKind(raw)}, not a string`);
      }
      values.push(raw);
      continue;
    }
    return fail(`item ${i} is ${jsonKind(item)}, not a string or an object`);
  }
  return { values, columns };
}

/**
 * summarizeListValueLoad says what the file actually contributed. Skipped values
 * are named rather than absorbed: a dropped row changes which rows come back, so
 * a silent count would misrepresent the result.
 */
export function summarizeListValueLoad(parsed: ListValueParse, loaded: number): string {
  if (parsed.error) return parsed.error;

  const parts = [`${loaded} values`];
  const skipped: string[] = [];
  if (parsed.rejected.length > 0) skipped.push(`${parsed.rejected.length} containing a comma`);
  if (parsed.skippedDuplicate > 0) skipped.push(`${parsed.skippedDuplicate} duplicate`);
  if (parsed.skippedBlank > 0) skipped.push(`${parsed.skippedBlank} blank`);
  if (skipped.length > 0) {
    const total = parsed.rejected.length + parsed.skippedDuplicate + parsed.skippedBlank;
    parts.push(`${total} skipped (${skipped.join(", ")})`);
  }
  return parts.join(" · ");
}

/** objectKeys returns the union of the keys of every object in the array. */
function objectKeys(items: unknown[]): string[] {
  const keys = new Set<string>();
  for (const item of items) {
    if (item === null || typeof item !== "object" || Array.isArray(item)) continue;
    for (const key of Object.keys(item as Record<string, unknown>)) keys.add(key);
  }
  return [...keys];
}

function readLines(text: string): read {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  // A trailing newline terminates the last line rather than starting a blank
  // one, so it must not be counted as a value the file skipped.
  if (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  return { values: lines, columns: [] };
}

function jsonKind(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return "an array";
  switch (typeof value) {
    case "object":
      return "an object";
    case "string":
      return "a string";
    case "number":
      return "a number";
    case "boolean":
      return "a boolean";
    default:
      return typeof value;
  }
}
