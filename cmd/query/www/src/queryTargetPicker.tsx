/**
 * The flat target a query runs against — an OpenSearch index, alias, data
 * stream, or the wildcard a daily rotation rolls up into. A hundred timestamped
 * index names is a searchable list, not a hierarchy, so this replaces the
 * catalog tree wherever the server declared a `targetLabel`.
 */

import { Combobox } from "@flanksource/clicky-ui";
import { useMemo } from "react";
import {
  openSearchIndexOptions,
  openSearchTargetKind,
  type Inspection,
} from "./connectionBrowserModel";

export function QueryTargetPicker({
  label,
  inspection,
  value,
  onChange,
}: {
  label: string;
  inspection: Inspection;
  value: string;
  onChange: (target: string, kind: string) => void;
}) {
  const options = useMemo(
    () => openSearchIndexOptions(inspection.data),
    [inspection.data],
  );
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <Combobox
        ariaLabel={label}
        label={label}
        value={value}
        onChange={(next) => onChange(next, openSearchTargetKind(inspection.data, next))}
        options={options}
        placeholder={
          inspection.error
            ? "Unavailable — check the connection"
            : `Select ${label.toLowerCase()} or type a wildcard…`
        }
        loading={inspection.loading}
        invalid={Boolean(inspection.error)}
        allowCustomValue
        className="min-w-0"
      />
      {inspection.error ? (
        <p role="alert" className="break-words text-xs text-destructive">
          {targetErrorMessage(inspection.error)}
        </p>
      ) : null}
    </div>
  );
}

function targetErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message.trim()) return error.message.trim();
  if (typeof error === "string" && error.trim()) return error.trim();
  return "The inspection request failed. Check the connection settings and try again.";
}
