import type { ProfileRowLimits } from "./connectionBrowserModel";

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
  hidden?: boolean;
  [key: string]: unknown;
};

export type ProfileProvider = {
  type?: string;
  connection?: string;
  options?: Record<string, unknown>;
  [key: string]: unknown;
};

export type ParamDraft = {
  name?: string;
  label?: string;
  type?: string;
  /** filter, limit, offset, time-from or time-to; empty behaves as filter. */
  role?: string;
  default?: unknown;
  options?: string[];
  required?: boolean;
  description?: string;
};

export type ProfileWizardDraft = Record<string, unknown> & {
  namespace?: string;
  profile?: string;
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
 * Every field the editors can show, in discovered order but each in its
 * configured form. A discovered field is a snapshot of what the source reported
 * — the configured one carries the user's edits, so it is the only version safe
 * to render into a control or to patch on top of. Fields that exist only in the
 * configuration (hand-added, or gone from a later sample) follow.
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
  const discoveredNames = new Set(discovered.map((field) => field.name));
  return [
    ...discovered.map(
      (field) =>
        configuredByName.get(field.name) ??
        configuredBySource.get(field.name) ??
        field,
    ),
    ...configured.filter(
      (field) =>
        !discoveredNames.has(field.name) &&
        !discoveredNames.has(field.source ?? ""),
    ),
  ];
}

export function applyVisibleFieldSelection(
  discovered: ProfileColumn[],
  configured: ProfileColumn[],
  visibleNames: Set<string>,
  selected: boolean,
): ProfileColumn[] {
  const configuredByName = new Map(
    configured.map((field) => [field.name, field]),
  );
  const selectedNames = new Set(configuredByName.keys());
  for (const name of visibleNames) {
    if (selected) selectedNames.add(name);
    else selectedNames.delete(name);
  }
  const discoveredNames = new Set(discovered.map((field) => field.name));
  return [
    ...discovered
      .filter((field) => selectedNames.has(field.name))
      .map((field) => configuredByName.get(field.name) ?? field),
    ...configured.filter((field) => !discoveredNames.has(field.name)),
  ];
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
