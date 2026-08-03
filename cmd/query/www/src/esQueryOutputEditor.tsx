/**
 * What a matching document comes back as: how many hits, where they start, which
 * _source fields travel, and whether the backend counts past its default.
 */

import { InputField } from "@flanksource/clicky-ui";
import { useState } from "react";
import type { EsSearch } from "./esQueryBuilderModel";

/**
 * pruneEmpty drops a sub-object that says nothing, so clearing the last field of
 * `source` removes `source` rather than storing `{}` the compiler must ignore.
 */
export function pruneEmpty<T extends object>(value: T): T | undefined {
  const said = Object.values(value).some((entry) =>
    Array.isArray(entry) ? entry.length > 0 : entry !== undefined && entry !== null,
  );
  return said ? value : undefined;
}

/** parseCount reads a non-negative integer, treating anything else as unset. */
export function parseCount(raw: string): number | undefined {
  if (raw.trim() === "") return undefined;
  const parsed = Number(raw);
  return Number.isInteger(parsed) && parsed >= 0 ? parsed : undefined;
}

export function EsQueryOutputEditor({
  search,
  onChange,
}: {
  search: EsSearch;
  onChange: (patch: Partial<EsSearch>) => void;
}) {
  const source = search.source ?? {};
  const total = search.trackTotalHits ?? {};
  const setSource = (patch: Partial<NonNullable<EsSearch["source"]>>) =>
    onChange({ source: pruneEmpty({ ...source, ...patch }) });
  const setTotal = (patch: Partial<NonNullable<EsSearch["trackTotalHits"]>>) =>
    onChange({ trackTotalHits: pruneEmpty({ ...total, ...patch }) });

  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-xs font-medium text-muted-foreground">Output</h3>
      <div className="flex flex-wrap items-center gap-2">
        <InputField
          aria-label="Size"
          className="w-28"
          type="number"
          placeholder="size"
          title="Maximum hits returned. A limit-role param overrides it."
          value={search.size === undefined ? "" : String(search.size)}
          onChange={(next) => onChange({ size: parseCount(next) })}
        />
        <InputField
          aria-label="From"
          className="w-28"
          type="number"
          placeholder="from"
          title="Hits to skip. Not available while scrolling."
          value={search.from === undefined ? "" : String(search.from)}
          onChange={(next) => onChange({ from: parseCount(next) })}
        />
        <label className="flex items-center gap-1 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={source.enabled !== false}
            onChange={(event) =>
              setSource({ enabled: event.target.checked ? undefined : false })
            }
          />
          Return _source
        </label>
        <label className="flex items-center gap-1 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={total.enabled === true}
            onChange={(event) =>
              setTotal({ enabled: event.target.checked || undefined })
            }
          />
          Track total hits
        </label>
        {total.enabled ? (
          <InputField
            aria-label="Total hits threshold"
            className="w-32"
            type="number"
            placeholder="threshold"
            title="Count matches accurately up to this many"
            value={total.threshold === undefined ? "" : String(total.threshold)}
            onChange={(next) => setTotal({ threshold: parseCount(next) })}
          />
        ) : null}
      </div>
      {source.enabled === false ? null : (
        <div className="flex flex-col gap-2">
          <PatternList
            label="Includes"
            values={source.includes ?? []}
            onChange={(includes) =>
              setSource({ includes: includes.length ? includes : undefined })
            }
          />
          <PatternList
            label="Excludes"
            values={source.excludes ?? []}
            onChange={(excludes) =>
              setSource({ excludes: excludes.length ? excludes : undefined })
            }
          />
        </div>
      )}
    </section>
  );
}

/**
 * A list of _source field patterns. They are patterns rather than field names —
 * `user.*` is the point of them — so this stays free text rather than a picker.
 */
function PatternList({
  label,
  values,
  onChange,
}: {
  label: string;
  values: string[];
  onChange: (values: string[]) => void;
}) {
  const [draft, setDraft] = useState("");
  const commit = () => {
    const parsed = draft
      .split(",")
      .map((entry) => entry.trim())
      .filter(Boolean);
    if (!parsed.length) return;
    onChange([...values, ...parsed]);
    setDraft("");
  };
  return (
    <div className="flex flex-wrap items-center gap-1">
      <span className="w-20 shrink-0 text-xs text-muted-foreground">{label}</span>
      {values.map((value, index) => (
        <span
          key={index}
          className="es-pattern-chip inline-flex items-center gap-1 rounded border bg-muted px-1.5 py-0.5 font-mono text-xs"
        >
          {value}
          <button
            type="button"
            aria-label={`Remove ${value}`}
            className="opacity-60 hover:opacity-100"
            onClick={() =>
              onChange(values.filter((_entry, position) => position !== index))
            }
          >
            ×
          </button>
        </span>
      ))}
      <InputField
        aria-label={`Add ${label.toLowerCase()} pattern`}
        className="min-w-40"
        placeholder="field or pattern…"
        value={draft}
        onChange={setDraft}
        onKeyDown={(event) => {
          if (event.key !== "Enter" && event.key !== ",") return;
          event.preventDefault();
          commit();
        }}
        onBlur={commit}
      />
    </div>
  );
}
