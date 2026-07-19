export type ProfileColumn = {
  name: string;
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

export type ProfileWizardDraft = Record<string, unknown> & {
  namespace?: string;
  profile?: string;
  provider?: ProfileProvider;
  query?: string;
  columns?: ProfileColumn[];
};

export type ProfileFieldFilter = {
  query: string;
  type: string;
  selection: "all" | "selected" | "unselected";
};

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
    Object.entries({ ...field, ...patch }).filter(([, value]) => value !== undefined),
  ) as ProfileColumn;
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

export function profileWizardStepReady(
  step: (typeof profileWizardSteps)[number]["id"],
  draft: ProfileWizardDraft,
  discovered: ProfileColumn[],
): boolean {
  if (step === "source") {
    return Boolean(draft.provider?.connection && draft.provider.type);
  }
  if (step === "query") {
    return Boolean(draft.query?.trim() && discovered.length > 0);
  }
  if (step === "fields") {
    return Boolean(draft.profile?.trim() && draft.columns?.length);
  }
  return (
    Boolean(draft.profile?.trim() && draft.provider?.connection) &&
    discovered.length > 0
  );
}
