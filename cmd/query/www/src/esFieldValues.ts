/**
 * What a field actually holds. The browser answers a terms aggregation over the
 * selected index, so an operand is picked from real values rather than typed
 * from memory. Which field the aggregation can run on is not always the field
 * being filtered — an analyzed text field aggregates through its keyword
 * sibling — and that resolution is owned here.
 */

import type { EsSearch } from "./esQueryBuilderModel";
import { fieldFamily, type EsFieldMapping } from "./esQueryOperators";

export type FieldValue = { value: string; count: number };

export type FieldValuesResult = {
  values: FieldValue[];
  total: number;
  /** Whether the values reflect the rest of the query or the whole index. */
  scoped: boolean;
};

/**
 * One resolved lookup. `key` identifies what is being asked — field and scope —
 * so a consumer can cache the answer without re-serializing the request.
 */
export type FieldValuesQuery = {
  key: string;
  fetch: (query: string) => Promise<FieldValuesResult>;
};

/** A host's lookup, bound to a connection and an index. */
export type FieldValuesSource = (request: {
  field: string;
  search?: EsSearch;
}) => FieldValuesQuery;

const valueLimit = 100;

/**
 * valueLookupField resolves the field a terms aggregation can run on, or
 * undefined when none can. A text field is analyzed, so its own doc values are
 * absent and the aggregation goes through the keyword sibling _field_caps
 * reports beside it. Dates are excluded deliberately: every timestamp is
 * distinct, so a value list says nothing the date-math presets do not.
 */
export function valueLookupField(
  fields: EsFieldMapping[],
  name: string | undefined,
): string | undefined {
  if (!name) return undefined;
  const field = fields.find((entry) => entry.name === name);
  if (!field || fieldFamily(field) === "date") return undefined;
  if (field.aggregatable !== false) return field.name;
  const keyword = fields.find(
    (entry) => entry.name === `${name}.keyword` && entry.aggregatable !== false,
  );
  return keyword?.name;
}

type ValuesRequestBody = {
  index: string;
  field: string;
  q?: string;
  limit?: number;
  search?: EsSearch;
  params?: Record<string, unknown>;
  roles?: Record<string, string>;
};

async function postValues(
  baseUrl: string,
  body: ValuesRequestBody,
): Promise<FieldValuesResult> {
  const response = await fetch(`${baseUrl}/values`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    const text = (await response.text()).trim();
    const error = new Error(text || `value lookup failed: ${response.status}`);
    (error as Error & { status?: number }).status = response.status;
    throw error;
  }
  return (await response.json()) as FieldValuesResult;
}

/**
 * makeFieldValueLookup binds a lookup to a connection and an index. A scope the
 * server cannot compile — a sibling condition left half-finished — is answered
 * across the whole index instead, and the result says so, so the widened scope
 * is visible rather than silently assumed. The scope is compiled server-side
 * against `params`, so a sibling condition that interpolates `{{.params.…}}`
 * narrows the suggestions the same way a run would.
 */
export function makeFieldValueLookup(options: {
  baseUrl: string;
  index: string;
  params?: Record<string, unknown>;
  roles?: Record<string, string>;
}): FieldValuesSource | undefined {
  const { baseUrl, index, params, roles } = options;
  if (!baseUrl || !index) return undefined;
  return ({ field, search }) => ({
    key: JSON.stringify([
      baseUrl,
      index,
      field,
      search ?? null,
      params ?? null,
      roles ?? null,
    ]),
    fetch: async (query) => {
      const body: ValuesRequestBody = {
        index,
        field,
        q: query,
        limit: valueLimit,
        ...(search ? { search } : {}),
        ...(params && Object.keys(params).length ? { params } : {}),
        ...(roles && Object.keys(roles).length ? { roles } : {}),
      };
      try {
        return await postValues(baseUrl, body);
      } catch (error) {
        const status = (error as Error & { status?: number }).status;
        if (!search || status !== 422) throw error;
        const { search: _dropped, ...unscoped } = body;
        return postValues(baseUrl, unscoped);
      }
    },
  });
}
