/**
 * Mirror of the Go `query/esdsl` specification. The builder edits this shape and
 * the server compiles it; nothing here renders DSL, so the two never drift.
 */

export type EsOccur = "filter" | "must" | "should" | "must_not";

/** A literal operand, or `{param}` binding one to a profile parameter. */
export type EsValue = unknown;

export type EsCondition = {
  op: string;
  occur?: EsOccur | "";
  field?: string;
  fields?: string[];
  value?: EsValue;
  values?: EsValue[];
  gt?: EsValue;
  gte?: EsValue;
  lt?: EsValue;
  lte?: EsValue;
  format?: string;
  timeZone?: string;
  analyzer?: string;
  matchOperator?: string;
  multiMatchType?: string;
  fuzziness?: string;
  slop?: number;
  boost?: number;
  caseInsensitive?: boolean;
  escape?: boolean;
  path?: string;
  scoreMode?: string;
  minimumShouldMatch?: string;
  conditions?: EsCondition[];
  optional?: boolean;
  when?: string;
};

export type EsSortBy = {
  field: string;
  order?: string;
  mode?: string;
  missing?: string;
  unmappedType?: string;
};

export type EsSearch = {
  query?: EsCondition;
  sort?: EsSortBy[];
  size?: number;
  from?: number;
  source?: { enabled?: boolean; includes?: string[]; excludes?: string[] };
  trackTotalHits?: { enabled?: boolean; threshold?: number };
  storedFields?: string[];
  fields?: string[];
  aggregations?: Record<string, unknown>;
  timeField?: string;
};

/** A path of child indexes from the root condition. `[]` is the root itself. */
export type ConditionPath = number[];

const defaultOperators: Record<string, string> = {
  keyword: "term",
  text: "match",
  date: "range",
  number: "range",
  boolean: "term",
  ip: "term",
  nested: "nested",
};

export function normalizeOccur(occur: string | undefined): EsOccur {
  return (occur || "filter") as EsOccur;
}

/**
 * defaultOperatorForFamily is the operator a family reads best as. It seeds a
 * new condition and leads the operator list, so both agree by construction.
 */
export function defaultOperatorForFamily(family: string): string {
  return defaultOperators[family] ?? "term";
}

export function emptyCondition(family = "keyword"): EsCondition {
  const op = defaultOperatorForFamily(family);
  return op === "nested" ? { op, conditions: [] } : { op };
}

export function emptyGroup(): EsCondition {
  return { op: "bool", conditions: [] };
}

export function conditionAt(
  root: EsCondition,
  path: ConditionPath,
): EsCondition | undefined {
  let node: EsCondition | undefined = root;
  for (const index of path) {
    node = node?.conditions?.[index];
    if (!node) return undefined;
  }
  return node;
}

/**
 * updateAt rebuilds only the branch it edits, so untouched subtrees keep their
 * identity and React re-renders just the changed rows.
 */
export function updateAt(
  root: EsCondition,
  path: ConditionPath,
  update: (condition: EsCondition) => EsCondition,
): EsCondition {
  if (path.length === 0) return update(root);
  const [index, ...rest] = path;
  return {
    ...root,
    conditions: (root.conditions ?? []).map((child, position) =>
      position === index ? updateAt(child, rest, update) : child,
    ),
  };
}

export function insertAt(
  root: EsCondition,
  groupPath: ConditionPath,
  position: number,
  condition: EsCondition,
): EsCondition {
  return updateAt(root, groupPath, (group) => {
    const children = [...(group.conditions ?? [])];
    children.splice(Math.min(Math.max(position, 0), children.length), 0, condition);
    return { ...group, conditions: children };
  });
}

export function removeAt(root: EsCondition, path: ConditionPath): EsCondition {
  if (path.length === 0) return emptyGroup();
  const index = path[path.length - 1];
  return updateAt(root, path.slice(0, -1), (group) => ({
    ...group,
    conditions: (group.conditions ?? []).filter(
      (_child, position) => position !== index,
    ),
  }));
}

/** isParamValue reports whether an operand binds a profile parameter. */
export function isParamValue(value: EsValue): value is { param: string } {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const keys = Object.keys(value as Record<string, unknown>);
  return (
    keys.length === 1 &&
    keys[0] === "param" &&
    typeof (value as { param: unknown }).param === "string"
  );
}

export function paramName(value: EsValue): string | undefined {
  return isParamValue(value) ? value.param : undefined;
}

/**
 * isEmptySpec reports whether a specification says nothing the raw query would
 * not. It is what decides whether a profile is still in raw mode.
 */
export function isEmptySpec(search: EsSearch | undefined): boolean {
  if (!search) return true;
  const { query, ...rest } = search;
  for (const value of Object.values(rest)) {
    if (Array.isArray(value) ? value.length > 0 : value !== undefined && value !== null) {
      if (typeof value === "object" && !Array.isArray(value)) {
        if (Object.keys(value as object).length > 0) return false;
        continue;
      }
      return false;
    }
  }
  return isEmptyCondition(query);
}

function isEmptyCondition(condition: EsCondition | undefined): boolean {
  if (!condition) return true;
  if (condition.op === "match_all") return true;
  if (condition.op !== "bool") return false;
  return (condition.conditions ?? []).every(isEmptyCondition);
}

export type QueryModeTransition = {
  search: EsSearch | undefined;
  query: string;
};

/**
 * toRawMode leaves the builder for the raw editor, seeding it with the DSL the
 * specification last compiled to. The specification is dropped, never kept
 * alongside the query: the server treats holding both as an authoring error.
 */
export function toRawMode(
  _search: EsSearch | undefined,
  compiled: string,
  currentQuery = "",
): QueryModeTransition {
  return { search: undefined, query: compiled.trim() || currentQuery };
}

/** toBuilderMode is the inverse: the raw query is dropped, not parsed. */
export function toBuilderMode(search?: EsSearch): QueryModeTransition {
  return { search: search ?? { query: emptyGroup() }, query: "" };
}
