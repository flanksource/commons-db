/**
 * The operand controls backed by a field's real values. Typing narrows the
 * terms aggregation server-side rather than filtering a fetched page, so a
 * field with thousands of values stays usable; a value outside the returned
 * window is still typeable, which is what `allowCustomValue` stands for.
 */

import { Combobox, type ComboboxOption } from "@flanksource/clicky-ui";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { FieldValuesQuery, FieldValuesResult } from "./esFieldValues";

function useFieldValues(lookup: FieldValuesQuery) {
  const [query, setQuery] = useState("");
  const result = useQuery<FieldValuesResult>({
    queryKey: ["es-field-values", lookup.key, query],
    queryFn: () => lookup.fetch(query),
    placeholderData: keepPreviousData,
    staleTime: 30_000,
    retry: 0,
  });
  return { ...result, onSearch: setQuery };
}

function valueOptions(result: FieldValuesResult | undefined): ComboboxOption[] {
  return (result?.values ?? []).map((entry) => ({
    value: entry.value,
    label: entry.value,
    title: `${entry.count.toLocaleString()} documents`,
  }));
}

/**
 * The summary explains what the list is a window onto: how much of the field's
 * cardinality is shown, and whether the rest of the query narrowed it.
 */
function valueSummary(
  result: FieldValuesResult | undefined,
  error: Error | null,
): string | undefined {
  if (error) return error.message;
  if (!result) return undefined;
  const scope = result.scoped ? "matching current filters" : "across the index";
  if (!result.values.length) return `no values ${scope}`;
  return `${result.values.length} of ${result.total.toLocaleString()} · ${scope}`;
}

export function ValueCombobox({
  id,
  label,
  lookup,
  value,
  onChange,
}: {
  id: string;
  label: string;
  lookup: FieldValuesQuery;
  value: string;
  onChange: (next: string) => void;
}) {
  const { data, error, isFetching, onSearch } = useFieldValues(lookup);
  const summary = valueSummary(data, error as Error | null);
  return (
    <Combobox
      id={id}
      ariaLabel={label}
      className="min-w-48"
      placeholder={label}
      value={value}
      onChange={onChange}
      options={valueOptions(data)}
      onSearch={onSearch}
      loading={isFetching}
      allowCustomValue
      {...(summary ? { footer: summary } : {})}
    />
  );
}

export function ValuesCombobox({
  label,
  lookup,
  values,
  onChange,
}: {
  label: string;
  lookup: FieldValuesQuery;
  values: string[];
  onChange: (next: string[]) => void;
}) {
  const { data, error, isFetching, onSearch } = useFieldValues(lookup);
  const summary = valueSummary(data, error as Error | null);
  return (
    <Combobox
      multiple
      variant="tags"
      ariaLabel={label}
      className="min-w-56 flex-1"
      placeholder={label}
      value={values}
      onChange={onChange}
      options={valueOptions(data)}
      onSearch={onSearch}
      loading={isFetching}
      allowCustomValue
      {...(summary ? { footer: summary } : {})}
    />
  );
}
