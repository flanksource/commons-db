/**
 * The include/exclude selection a tri-state filter holds, and the wire form it
 * travels in: comma-joined, with `!` marking an exclusion and no escaping.
 *
 * clicky-ui has its own copy of this codec but does not export it, so it is
 * reimplemented here against the authority that actually matters — the Go
 * decoder in `query.parseColumnFilterSelection`, which is what the server reads.
 * Note it re-trims after stripping `!`, so " ! eu " and "!eu" mean the same
 * thing; this does too.
 *
 * One deliberate divergence: Go rejects a bare `!` as an empty exclusion, while
 * this drops it. The control can never produce one, and dropping it keeps a
 * hand-edited URL from breaking the page before the server ever sees it.
 */

export type MultiFilterMode = "include" | "exclude";
export type MultiFilterValue = Record<string, MultiFilterMode>;

export function parseMultiFilterValue(value: string): MultiFilterValue {
  const parsed: MultiFilterValue = {};
  for (const segment of value.split(",")) {
    const item = segment.trim();
    if (item === "") continue;
    if (item.startsWith("!")) {
      const excluded = item.slice(1).trim();
      if (excluded !== "") parsed[excluded] = "exclude";
      continue;
    }
    parsed[item] = "include";
  }
  return parsed;
}

/** serializeMultiFilterValue writes includes first, then exclusions, so the same
 * selection always produces the same string (and the same URL). */
export function serializeMultiFilterValue(value: MultiFilterValue): string {
  const includes: string[] = [];
  const excludes: string[] = [];
  for (const [item, mode] of Object.entries(value)) {
    if (mode === "exclude") excludes.push("!" + item);
    else includes.push(item);
  }
  return [...includes, ...excludes].join(",");
}

export type MergeStrategy = "replace" | "add";

export type MergeResult = {
  next: MultiFilterValue;
  added: number;
  flipped: number;
};

/**
 * mergeIntoMultiFilter folds freshly-loaded values into the current selection.
 *
 * `replace` is the default for a file load — "here are my 4,000 account ids"
 * means those, not those plus whatever was already picked. A value carrying its
 * own `!` prefix is excluded whatever mode was chosen for the rest of the file,
 * so a file may mix the two exactly as a typed selection may.
 */
export function mergeIntoMultiFilter(
  current: MultiFilterValue,
  values: string[],
  mode: MultiFilterMode,
  strategy: MergeStrategy,
): MergeResult {
  const next: MultiFilterValue = strategy === "add" ? { ...current } : {};
  let added = 0;
  let flipped = 0;

  for (const raw of values) {
    let item = raw.trim();
    let itemMode = mode;
    if (item.startsWith("!")) {
      item = item.slice(1).trim();
      itemMode = "exclude";
    }
    if (item === "") continue;

    const before = next[item];
    if (before === undefined) added++;
    else if (before !== itemMode) flipped++;
    next[item] = itemMode;
  }
  return { next, added, flipped };
}
