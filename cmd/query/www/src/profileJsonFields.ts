import type {
  ClickyColumn,
  ClickyNode,
  ClickyRow,
} from "@flanksource/clicky-ui/clicky";
import type { ProfileColumn } from "@flanksource/clicky-ui/profiles";

const structuredColumnTypes = new Set(["json", "key_value", "key_values"]);

export type JsonFieldCandidate = {
  id: string;
  source: string;
  name: string;
  type: string;
  sample: string | number | boolean;
  cel: string;
};

export function discoverJsonFieldCandidates(
  columns: ClickyColumn[],
  row: ClickyRow,
): JsonFieldCandidate[] {
  const seen = new Set<string>();
  return columns.flatMap((column) => {
    if (!structuredColumnTypes.has(column.type ?? "")) return [];
    const node = row.cells[column.name];
    const map = mapNodeEntries(node);
    const parsed = parseNodeValue(node);
    let candidates: JsonFieldCandidate[] = [];
    if (column.type === "key_value") {
      candidates = map.length
        ? mapEntryCandidates(column.name, map, "object")
        : objectCandidates(column.name, parsed);
    } else if (column.type === "key_values") {
      candidates = map.length
        ? mapEntryCandidates(column.name, map, "key-value")
        : Array.isArray(parsed)
          ? keyValueCandidates(column.name, parsed)
          : [];
    } else if (Array.isArray(parsed)) {
      candidates = keyValueCandidates(column.name, parsed);
    } else {
      candidates = objectCandidates(column.name, parsed);
    }
    return candidates
      .filter((candidate) => {
        if (seen.has(candidate.id) || seen.has(candidate.cel)) return false;
        seen.add(candidate.id);
        seen.add(candidate.cel);
        return true;
      })
      .sort((left, right) => left.name.localeCompare(right.name));
  });
}

export function configuredColumnName(
  candidate: JsonFieldCandidate,
  columns: ProfileColumn[],
): string | undefined {
  return columns.find((column) => column.cel === candidate.cel)?.name;
}

export function promotedColumnsError(
  existing: ProfileColumn[],
  additions: ProfileColumn[],
): string | null {
  const names = new Set(existing.map((column) => column.name));
  for (const column of additions) {
    const name = column.name.trim();
    if (!name) return "Every promoted column needs a name";
    if (names.has(name)) return `Column name "${name}" is already configured`;
    names.add(name);
  }
  return null;
}

export function mergePromotedColumns(
  existing: ProfileColumn[],
  additions: ProfileColumn[],
): ProfileColumn[] {
  const error = promotedColumnsError(existing, additions);
  if (error) throw new Error(error);
  return [...existing, ...additions.map((column) => ({ ...column, name: column.name.trim() }))];
}

function objectCandidates(
  source: string,
  value: unknown,
): JsonFieldCandidate[] {
  if (!isRecord(value)) return [];
  const candidates: JsonFieldCandidate[] = [];
  const visit = (current: Record<string, unknown>, path: string[]) => {
    for (const [name, child] of Object.entries(current)) {
      const nextPath = [...path, name];
      if (isRecord(child)) {
        visit(child, nextPath);
      } else if (isScalar(child)) {
        candidates.push({
          id: candidateID(source, "object", nextPath.join(".")),
          source,
          name: nextPath.join("."),
          type: inferColumnType(child),
          sample: child,
          cel: objectCEL(source, nextPath),
        });
      }
    }
  };
  visit(value, []);
  return candidates;
}

function keyValueCandidates(
  source: string,
  value: unknown[],
): JsonFieldCandidate[] {
  if (
    value.length === 0 ||
    !value.every(
      (item) =>
        isRecord(item) &&
        typeof item.key === "string" &&
        item.key.trim() !== "" &&
        isScalar(item.value),
    )
  ) {
    return [];
  }
  return value.map((raw) => {
    const item = raw as Record<string, unknown>;
    const key = item.key as string;
    const sample = item.value as string | number | boolean;
    return {
      id: candidateID(source, "key-value", key),
      source,
      name: key,
      type: inferColumnType(sample, typeof item.type === "string" ? item.type : ""),
      sample,
      cel: keyValueCEL(source, key),
    };
  });
}

function mapEntryCandidates(
  source: string,
  entries: Array<{ name: string; value: unknown }>,
  kind: "object" | "key-value",
): JsonFieldCandidate[] {
  return entries.flatMap(({ name, value }) => {
    if (!name.trim() || !isScalar(value)) return [];
    return [
      {
        id: candidateID(source, kind, name),
        source,
        name,
        type: inferColumnType(value),
        sample: value,
        cel:
          kind === "object"
            ? objectCEL(source, [name])
            : keyValueCEL(source, name),
      },
    ];
  });
}

function objectCEL(source: string, path: string[]): string {
  const sourceLiteral = celSingleQuoted(source);
  const jsonPath = `$${path.map(jsonPathSegment).join("")}`;
  return `${sourceLiteral} in row ? jsonpath(${JSON.stringify(jsonPath)}, type(row[${sourceLiteral}]) == string ? row[${sourceLiteral}].JSON() : row[${sourceLiteral}]) : ''`;
}

function keyValueCEL(source: string, key: string): string {
  const sourceLiteral = celSingleQuoted(source);
  const escapedKey = key.replace(/\\/g, "\\\\").replace(/'/g, "\\'");
  const jsonPath = `$[?(@.key == '${escapedKey}')].value`;
  return `${sourceLiteral} in row ? jsonpath(${JSON.stringify(jsonPath)}, type(row[${sourceLiteral}]) == string ? row[${sourceLiteral}].JSONArray() : row[${sourceLiteral}]) : ''`;
}

function jsonPathSegment(value: string): string {
  return `['${value.replace(/\\/g, "\\\\").replace(/'/g, "\\'")}']`;
}

function celSingleQuoted(value: string): string {
  return `'${value.replace(/\\/g, "\\\\").replace(/'/g, "\\'")}'`;
}

function inferColumnType(value: string | number | boolean, hint = ""): string {
  const normalized = hint.toLowerCase();
  if (normalized.includes("bool")) return "boolean";
  if (/int|long|float|double|number|decimal/.test(normalized)) return "number";
  if (/date|time/.test(normalized)) return "datetime";
  if (typeof value === "boolean") return "boolean";
  if (typeof value === "number") return "number";
  if (/^\d{4}-\d{2}-\d{2}t/i.test(value) && !Number.isNaN(Date.parse(value))) {
    return "datetime";
  }
  return "string";
}

function parseNodeValue(node: ClickyNode | undefined): unknown {
  if (!node) return null;
  const semanticChild = node.children?.find((child) =>
    ["map", "list", "code"].includes(child.kind),
  );
  if (semanticChild) return parseNodeValue(semanticChild);
  if (node.kind === "map") {
    return Object.fromEntries(mapNodeEntries(node).map(({ name, value }) => [name, value]));
  }
  if (node.kind === "list") {
    return (node.items ?? []).map(parseNodeValue);
  }
  const text = (node.kind === "code" ? node.source : node.plain ?? node.text)?.trim();
  if (!text) return null;
  try {
    return JSON.parse(text);
  } catch {
    return scalarText(text);
  }
}

function mapNodeEntries(
  node: ClickyNode | undefined,
): Array<{ name: string; value: unknown }> {
  if (!node) return [];
  if (node.kind !== "map") {
    const map = node.children?.find((child) => child.kind === "map");
    return mapNodeEntries(map);
  }
  return (node.fields ?? []).map((field) => ({
    name: field.name || field.label || "",
    value: parseNodeValue(field.value),
  }));
}

function scalarText(value: string): string | number | boolean {
  if (value === "true") return true;
  if (value === "false") return false;
  if (/^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:e[+-]?\d+)?$/i.test(value)) {
    return Number(value);
  }
  return value;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function isScalar(value: unknown): value is string | number | boolean {
  return typeof value === "string" || typeof value === "number" || typeof value === "boolean";
}

function candidateID(source: string, kind: string, path: string): string {
  return `${source}\u0000${kind}\u0000${path}`;
}
