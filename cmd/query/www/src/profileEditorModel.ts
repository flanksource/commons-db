import type { JsonSchemaObject } from "@flanksource/clicky-ui";
import profileSchemaDocument from "../../../../schemas/profile.json";
import { validateProfileParams } from "./profileParamModel";
import type { ProfileColumn, ProfileWizardDraft } from "./profileWizardModel";

type BundledProfileSchema = JsonSchemaObject & {
  $defs?: Record<string, JsonSchemaObject>;
};

const profileSchema = profileSchemaDocument as unknown as BundledProfileSchema;
export const profileEditorSchema = profileSchema;

export const profileEditorSections = [
  { id: "general", label: "General", hint: "Name, namespace, render mode" },
  { id: "source", label: "Source & Query", hint: "Provider, connection, sample" },
  { id: "columns", label: "Columns", hint: "Fields, labels, expressions" },
  { id: "parameters", label: "Parameters", hint: "Named query inputs" },
  { id: "advanced", label: "Advanced", hint: "Imports, aliases, processors, output" },
  { id: "raw", label: "Raw YAML", hint: "Edit the document directly" },
] as const;

export type ProfileEditorSection = (typeof profileEditorSections)[number]["id"];

export const profileAdvancedKeys = ["imports", "aliases", "ignore", "processors", "output"];

export type ProfileSectionStatus = { badge?: string; attention?: boolean };

/**
 * Rail annotations per section. The route replaced tabs with a vertical rail,
 * which has room to say how much each section holds — so a stale sample or an
 * empty column set is visible without opening the section.
 */
export function profileEditorSectionStatus({
  draft,
  availableColumns,
  sampleStale,
}: {
  draft: ProfileWizardDraft;
  availableColumns: number;
  sampleStale: boolean;
}): Record<ProfileEditorSection, ProfileSectionStatus> {
  const configured = draft.columns?.length ?? 0;
  const params = Array.isArray(draft.params) ? draft.params.length : 0;
  const advanced = profileAdvancedKeys.filter((key) =>
    Object.prototype.hasOwnProperty.call(draft, key),
  ).length;
  return {
    general: { attention: !draft.profile?.trim() },
    source: {
      badge: draft.provider?.type || undefined,
      attention: sampleStale || !draft.provider?.type?.trim(),
    },
    columns: {
      badge: `${configured}/${Math.max(availableColumns, configured)}`,
      attention: configured === 0,
    },
    parameters: { badge: params ? String(params) : undefined },
    advanced: { badge: advanced ? String(advanced) : undefined },
    raw: {},
  };
}

export function cloneProfileDraft(
  value: Record<string, unknown>,
): ProfileWizardDraft {
  return structuredClone(value) as ProfileWizardDraft;
}

export function profileSchemaProjection(keys: string[]): JsonSchemaObject {
  const properties = Object.fromEntries(
    keys.flatMap((key) => {
      const property = profileSchema.properties?.[key];
      return property ? [[key, property]] : [];
    }),
  );
  return {
    type: "object",
    properties,
    required: (profileSchema.required ?? []).filter((key) => keys.includes(key)),
  };
}

export function providerOptionsSchema(providerType: string): JsonSchemaObject {
  const definition = profileSchema.$defs?.[providerType];
  const options = definition?.properties?.options;
  if (!options || options.type !== "object") {
    return { type: "object", properties: {}, additionalProperties: true };
  }
  return options as JsonSchemaObject;
}

export function providerTypes(): string[] {
  const values = profileSchema.properties?.provider?.properties?.type?.enum;
  return Array.isArray(values)
    ? values.filter((value): value is string => typeof value === "string")
    : [];
}

export function mergeProfileProjection(
  draft: ProfileWizardDraft,
  keys: string[],
  next: Record<string, unknown>,
): ProfileWizardDraft {
  const merged = { ...draft };
  for (const key of keys) delete merged[key];
  for (const key of keys) {
    if (Object.prototype.hasOwnProperty.call(next, key)) merged[key] = next[key];
  }
  return merged;
}

export function mergeSampledProfileColumns(
  configured: ProfileColumn[],
  sampled: ProfileColumn[],
): ProfileColumn[] {
  const configuredNames = new Set(configured.map((column) => column.name));
  return [
    ...configured.map((column) => ({ ...column })),
    ...sampled
      .filter((column) => !configuredNames.has(column.name))
      .map((column) => ({ ...column })),
  ];
}

export function resetProfileColumns(
  draft: ProfileWizardDraft,
  sampled: ProfileColumn[],
): ProfileWizardDraft {
  if (sampled.length === 0) {
    throw new Error("Cannot reset profile columns without sampled columns");
  }
  return { ...draft, columns: structuredClone(sampled) };
}

export function profileColumnResetState({
  providerType,
  sampledColumnCount,
  sampleStale,
}: {
  providerType: string;
  sampledColumnCount: number;
  sampleStale: boolean;
}) {
  if (providerType !== "opensearch") {
    return { visible: false, disabled: true, title: "" };
  }
  if (sampledColumnCount === 0) {
    return {
      visible: true,
      disabled: true,
      title: "Run a sample before resetting columns",
    };
  }
  if (sampleStale) {
    return {
      visible: true,
      disabled: true,
      title: "Run another sample for the current source and query",
    };
  }
  return {
    visible: true,
    disabled: false,
    title: "Replace configured columns with the latest sample",
  };
}

export function profileSampleSignature(draft: ProfileWizardDraft): string {
  return JSON.stringify({
    provider: draft.provider ?? {},
    query: draft.query ?? "",
  });
}

export function validateProfileEditorDraft(
  draft: ProfileWizardDraft,
): string | null {
  if (!draft.profile?.trim()) return "Profile name is required";
  if (!draft.provider?.type?.trim()) return "Provider type is required";
  const names = new Set<string>();
  for (const column of draft.columns ?? []) {
    const name = column.name.trim();
    if (!name) return "Every column needs a name";
    if (names.has(name)) return `Column name "${name}" is duplicated`;
    names.add(name);
  }
  return validateProfileParams(draft.params, draft.provider?.type);
}

export function profileUpdateConflictTarget(error: string): string | null {
  if (!error.includes("PROFILE_NAME_CONFLICT")) return null;
  return error.match(/existing profile "([^"]+)"/)?.[1] ?? null;
}

export function profileRoute(name: string): string {
  const slug = Array.from(name.trim().toLowerCase())
    .map((character) =>
      /[a-z0-9]/.test(character)
        ? character
        : /[ ._\/-]/.test(character)
          ? "-"
          : "",
    )
    .join("")
    .replace(/^-+|-+$/g, "");
  return `/profile-${slug}`;
}

/** Deep-linkable editor route for a profile surface (`profile-os2`). */
export function profileEditRoute(surfaceKey: string): string {
  return `/${surfaceKey}/edit`;
}

/** Surface key an edit route addresses, or null when the path is not one. */
export function profileEditSurfaceKey(pathname: string): string | null {
  return pathname.match(/^\/(profile-[^/]+)\/edit\/?$/)?.[1] ?? null;
}
