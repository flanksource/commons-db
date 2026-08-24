/**
 * Author-time checks on a profile's parameters, mirroring what the server
 * rejects (query.validateParams) so a mistake is caught in the editor rather
 * than on the first run — and so the failures that would otherwise be *silent*
 * are caught at all: a parameter named after a reserved request key simply never
 * receives a value.
 */

import type { ParamDraft } from "./profileWizardModel";

/** Query-string keys the server consumes as transport concerns before params are
 *  built (see IsReservedParam in cmd/query/profiles/execution.go). */
const RESERVED_REQUEST_KEYS = [
  "format",
  "scope",
  "page",
  "limit",
  "offset",
  "filename",
  "_download",
  "args",
  "__schema",
  "__lookup",
  "__lookup_filter",
  "__lookup_q",
];

const COLUMN_FILTER_PREFIX = "filter.";

/** Providers that turn include/exclude selections into backend query clauses
 *  (query.SupportsNativeFilters). */
const NATIVE_FILTER_PROVIDERS = ["opensearch", "opentelemetry"];

export function validateProfileParams(
  params: ParamDraft[] | undefined,
  providerType: string | undefined,
): string | null {
  const seen = new Set<string>();
  for (const param of params ?? []) {
    const name = param.name?.trim() ?? "";
    if (!name) return "Every parameter needs a name";
    if (seen.has(name)) return `Parameter name "${name}" is duplicated`;
    seen.add(name);

    if (name.startsWith(COLUMN_FILTER_PREFIX)) {
      return `Parameter "${name}" must not start with "${COLUMN_FILTER_PREFIX}", which is reserved for column filters`;
    }
    // limit and offset are exactly the keys a profile may rename by claiming
    // their role, so only a plain filter parameter collides.
    const isFilter = param.role === undefined || param.role === "filter";
    if (isFilter && RESERVED_REQUEST_KEYS.includes(name)) {
      return `Parameter "${name}" collides with a reserved request key and would never receive a value`;
    }

    const error = validateOneParam(param, name, providerType);
    if (error) return error;
  }
  return null;
}

function validateOneParam(
  param: ParamDraft,
  name: string,
  providerType: string | undefined,
): string | null {
  if (param.type === "list" && param.role !== undefined && param.role !== "filter") {
    return `Parameter "${name}" is a list, which cannot take the "${param.role}" role`;
  }

  const field = param.field?.trim();
  if (field) {
    if (param.type !== "list") {
      return `Parameter "${name}" sets a field but is not a list; only a list binds to a backend field`;
    }
    if (!NATIVE_FILTER_PROVIDERS.includes(providerType ?? "")) {
      return `Parameter "${name}" binds to field "${field}", but provider "${providerType ?? ""}" applies no native filters, so an excluded value would be dropped`;
    }
  }

  // A list's values travel comma-joined with "!" marking an exclusion, and the
  // wire form has no escape — an option carrying either could never be selected.
  if (param.type === "list") {
    for (const option of param.options ?? []) {
      if (option.includes(",")) {
        return `Parameter "${name}" option "${option}" contains a comma, which separates values on the wire`;
      }
      if (option.startsWith("!")) {
        return `Parameter "${name}" option "${option}" starts with !, which marks an exclusion on the wire`;
      }
    }
  }

  return validateDefault(param, name);
}

function validateDefault(param: ParamDraft, name: string): string | null {
  const options = param.options ?? [];
  if (options.length === 0 || param.default === undefined || param.default === null) return null;

  const chosen = Array.isArray(param.default) ? param.default : [param.default];
  for (const value of chosen) {
    if (typeof value !== "string") continue;
    if (!options.includes(value)) {
      return `Parameter "${name}" default "${value}" is not one of its options`;
    }
  }
  return null;
}
