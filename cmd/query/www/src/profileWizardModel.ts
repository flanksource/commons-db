import type { ProfileRowLimits } from "./connectionBrowserModel";

/** How a column is filtered at the backend; every field overrides an inference
 *  the server would otherwise make from the column itself. */
export type ProfileColumnFilter = {
  /** Backend field the selection applies to; blank infers it from the column. */
  field?: string;
  /** terms, range, time, boolean, text or none; blank infers it from the type. */
  kind?: string;
  /** Enumerated values, replacing the backend lookup. */
  options?: string[];
  /** Ask the backend for this field's distinct values. */
  lookup?: boolean;
  /** How many of those values the control offers before the rest are typed for. */
  limit?: number;
  /** Allow several values at once. */
  multi?: boolean;
  /** Offer no filter while keeping the column rendered. */
  disabled?: boolean;
};

export type ProfileColumn = {
  name: string;
  source?: string;
  label?: string;
  type?: string;
  kind?: string;
  format?: string;
  unit?: string;
  width?: number;
  cel?: string;
  /** Path computing the value, rooted at the row or at `source` when set. */
  jsonpath?: string;
  hidden?: boolean;
  filter?: ProfileColumnFilter;
  [key: string]: unknown;
};

export type ProfileProvider = {
  type?: string;
  connection?: string;
  options?: Record<string, unknown>;
  [key: string]: unknown;
};

/** The parameter types the profile schema accepts. */
export type ParamDraftType = "string" | "number" | "boolean" | "date" | "enum" | "list";

/** filter, limit, offset, time-from or time-to; empty behaves as filter. */
export type ParamDraftRole = "filter" | "limit" | "offset" | "time-from" | "time-to";

export type ParamDraft = {
  name?: string;
  label?: string;
  type?: ParamDraftType;
  role?: ParamDraftRole;
  default?: unknown;
  options?: string[];
  required?: boolean;
  description?: string;
  /** Value rewrite; {value} is the supplied value. */
  template?: string;
  /** Backend field a list parameter filters on. Setting it lets a value be
   *  excluded as well as included; it requires an OpenSearch-backed provider. */
  field?: string;
};

/** PARAM_TYPES_WITH_OPTIONS are the types whose values come from a fixed set, so
 *  the editor offers an options picker for them. */
export const PARAM_TYPES_WITH_OPTIONS: ParamDraftType[] = ["enum", "list"];

export function paramHasOptions(param: ParamDraft): boolean {
  return param.type !== undefined && PARAM_TYPES_WITH_OPTIONS.includes(param.type);
}

export type ProfileWizardDraft = Record<string, unknown> & {
  namespace?: string;
  profile?: string;
  /** Overrides the sidebar/picker glyph the provider type would otherwise give. */
  icon?: string;
  provider?: ProfileProvider;
  query?: string;
  columns?: ProfileColumn[];
  /** The inputs a run binds — by name in the query, the provider options or the
   *  connection. The query workspace previews against their defaults. */
  params?: ParamDraft[];
  /** The row caps this profile sets for itself; unset ones take their default. */
  limits?: ProfileRowLimits;
};

/**
 * A profile that caps nothing carries no block at all, so the defaults are what
 * it inherits rather than what it froze at the moment it was edited. Both draft
 * hosts write the caps through this, so neither can leave an empty map behind.
 */
export function withProfileLimits<T extends ProfileWizardDraft>(
  draft: T,
  limits: ProfileRowLimits | undefined,
): T {
  const next = { ...draft };
  if (limits) next.limits = limits;
  else delete next.limits;
  return next;
}

export type ProfileFieldFilter = {
  query: string;
  type: string;
  selection: "all" | "selected" | "unselected";
};

export const PROFILE_COLUMN_FORMAT_OPTIONS = [
  { value: "date", label: "Date/time" },
  { value: "float", label: "Number" },
  { value: "duration", label: "Duration" },
  { value: "bytes", label: "Bytes" },
  { value: "currency", label: "Currency" },
] as const;

export const PROFILE_COLUMN_UNIT_OPTIONS = [
  { value: "none", label: "Compact count" },
  { value: "short", label: "Short number" },
  { value: "percent", label: "Percent (0-100)" },
  { value: "percentunit", label: "Percent (0-1)" },
  { value: "bytes", label: "Bytes (IEC)" },
  { value: "decbytes", label: "Bytes (SI)" },
  { value: "Bps", label: "Bytes/sec" },
  { value: "binBps", label: "Binary bytes/sec" },
  { value: "ms", label: "Milliseconds" },
  { value: "s", label: "Seconds" },
] as const;

export const profileWizardSteps = [
  { id: "source", label: "Choose source", description: "Connection" },
  { id: "query", label: "Explore & sample", description: "Query" },
  { id: "fields", label: "Name & shape", description: "Fields" },
  { id: "review", label: "Review", description: "Save" },
] as const;

export function filterProfileFields(
  fields: ProfileColumn[],
  selectedNames: Set<string>,
  filter: ProfileFieldFilter,
): ProfileColumn[] {
  const query = filter.query.trim().toLowerCase();
  return fields.filter((field) => {
    const selected = selectedNames.has(field.name);
    if (filter.selection === "selected" && !selected) return false;
    if (filter.selection === "unselected" && selected) return false;
    if (filter.type && field.type !== filter.type) return false;
    if (!query) return true;
    return `${field.name} ${field.label ?? ""}`.toLowerCase().includes(query);
  });
}

/**
 * Every field the editors can show, each in its configured form. A discovered
 * field is a snapshot of what the source reported — the configured one carries
 * the user's edits, so it is the only version safe to render into a control or
 * to patch on top of.
 *
 * The configuration owns the order, because it is the order the profile renders
 * its columns in and the one the user drags rows into. A discovered field that
 * was never configured (not selected, or dropped from the profile) has no place
 * of its own, so it keeps the one it had: it is anchored just after whichever
 * configured field preceded it in the sample.
 */
export function availableProfileFields(
  discovered: ProfileColumn[],
  configured: ProfileColumn[],
): ProfileColumn[] {
  const configuredByName = new Map(
    configured.map((field) => [field.name, field]),
  );
  const configuredBySource = new Map(
    configured
      .filter((field) => field.source)
      .map((field) => [field.source as string, field]),
  );
  const ordered = [...configured];
  let anchor = 0;
  for (const field of discovered) {
    const match =
      configuredByName.get(field.name) ?? configuredBySource.get(field.name);
    if (match) {
      anchor = ordered.indexOf(match) + 1;
      continue;
    }
    ordered.splice(anchor, 0, field);
    anchor += 1;
  }
  return ordered;
}

/**
 * Moves the named column onto the target's position, shifting the columns
 * between them — the drop semantics the field grid's row drag needs. Columns
 * are addressed by name because a drag only ever carries the row's identity.
 * A name that is not configured has no position, so the order is returned
 * unchanged rather than guessed at.
 */
export function reorderProfileColumns(
  columns: ProfileColumn[],
  sourceName: string,
  targetName: string,
): ProfileColumn[] {
  const from = columns.findIndex((column) => column.name === sourceName);
  const to = columns.findIndex((column) => column.name === targetName);
  if (from < 0 || to < 0 || from === to) return columns;
  const next = [...columns];
  next.splice(to, 0, ...next.splice(from, 1));
  return next;
}

/**
 * Selects or deselects the named fields, keeping the configured order intact —
 * a re-selected field returns to where the grid already showed it rather than
 * to the back of the list or to its position in the sample.
 */
export function applyVisibleFieldSelection(
  discovered: ProfileColumn[],
  configured: ProfileColumn[],
  visibleNames: Set<string>,
  selected: boolean,
): ProfileColumn[] {
  const selectedNames = new Set(configured.map((field) => field.name));
  for (const name of visibleNames) {
    if (selected) selectedNames.add(name);
    else selectedNames.delete(name);
  }
  return availableProfileFields(discovered, configured).filter((field) =>
    selectedNames.has(field.name),
  );
}

/** The control kinds a column filter can render as, with the server's own
 *  wording. Mirrors query.ColumnFilterKindValues() and the x-enum-labels the
 *  profile schema carries for them. */
export const PROFILE_FILTER_KIND_OPTIONS = [
  { value: "terms", label: "Value selection" },
  { value: "range", label: "Numeric range" },
  { value: "time", label: "Time range" },
  { value: "boolean", label: "Yes/no" },
  { value: "text", label: "Substring" },
  { value: "none", label: "Not filterable" },
] as const;

/** The server's own default, so an unset limit can be shown for what it does.
 *  Mirrors query.DefaultFilterLookupLimit. */
export const PROFILE_FILTER_DEFAULT_LIMIT = 50;

/** Mirrors query.MaxFilterLookupLimit — the largest head a lookup will serve. */
export const PROFILE_FILTER_MAX_LIMIT = 200;

/**
 * Merges one filter knob into a column's filter block, dropping the block once
 * nothing is left in it.
 *
 * The distinction matters on save: `filter: {}` is a declaration that declares
 * nothing, and the server reads a present-but-empty block differently from an
 * absent one — unchecking the last box has to mean "infer this again", not
 * "override it with silence".
 */
export function patchColumnFilter(
  filter: ProfileColumnFilter | undefined,
  patch: Partial<ProfileColumnFilter>,
): ProfileColumnFilter | undefined {
  const merged = Object.fromEntries(
    Object.entries({ ...filter, ...patch }).filter(([, value]) => value !== undefined),
  ) as ProfileColumnFilter;
  return Object.keys(merged).length ? merged : undefined;
}

/** What the server picks for a column that declares no filter kind. Mirrors
 *  columnFilterKindFor; text is absent there on purpose, so it is here too. */
export function inferredFilterKind(column: ProfileColumn): string {
  switch (column.type) {
    case "number":
    case "duration":
    case "bytes":
      return "range";
    case "datetime":
      return "time";
    case "boolean":
      return "boolean";
    case "key_value":
    case "key_values":
    case "json":
      return "none";
    default:
      return "terms";
  }
}

export function patchProfileField(
  field: ProfileColumn,
  patch: Partial<ProfileColumn>,
): ProfileColumn {
  return Object.fromEntries(
    Object.entries({ ...field, ...patch }).filter(
      ([, value]) => value !== undefined,
    ),
  ) as ProfileColumn;
}

export function renameProfileField(
  field: ProfileColumn,
  name: string,
): ProfileColumn {
  const source = field.source ?? (field.cel ? undefined : field.name);
  return patchProfileField(field, {
    name,
    source: source === name ? undefined : source,
  });
}

export function providerTypeFromConnectionLabel(label: string): string | null {
  const match = label.match(/\(([^()]+)\)\s*$/);
  return match?.[1]?.trim() || null;
}

export function profileConnectionID(value: string): string | null {
  const prefix = "connection://";
  if (!value.startsWith(prefix)) return null;
  return value.slice(prefix.length).trim() || null;
}

export function profileWizardErrorMessage(
  error: unknown,
  fallback: string,
): string {
  return error instanceof Error && error.message.trim()
    ? error.message.trim()
    : fallback;
}

/**
 * A profile says what to fetch either as a raw query or as a structured search
 * specification — never both, which is why this is an either/or rather than a
 * check on `query` alone.
 */
export function profileWizardHasQuery(draft: ProfileWizardDraft): boolean {
  return Boolean(
    draft.query?.trim() || draft.provider?.options?.search !== undefined,
  );
}

export function profileWizardStepReady(
  step: (typeof profileWizardSteps)[number]["id"],
  draft: ProfileWizardDraft,
  discovered: ProfileColumn[],
): boolean {
  if (step === "source") {
    return Boolean(draft.provider?.connection && draft.provider.type);
  }
  if (step === "query") {
    return Boolean(profileWizardHasQuery(draft) && discovered.length > 0);
  }
  if (step === "fields") {
    return Boolean(draft.profile?.trim() && draft.columns?.length);
  }
  return (
    Boolean(draft.profile?.trim() && draft.provider?.connection) &&
    discovered.length > 0
  );
}
