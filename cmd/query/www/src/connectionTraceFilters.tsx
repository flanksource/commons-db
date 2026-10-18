import type { useInspection } from "@flanksource/clicky-ui/profiles";
import type { TraceFilters } from "./connectionTraceApi";

const INPUT_CLASS = "h-8 rounded-md border bg-background px-2 text-sm";

const LIST_FIELDS = [
  { key: "users", label: "Users" },
  { key: "apps", label: "Applications" },
  { key: "hosts", label: "Hosts" },
  { key: "events", label: "Events" },
] as const;

const DURATION_FIELDS = [
  { key: "minDuration", label: "Min duration", placeholder: "e.g. 100ms" },
  { key: "maxDuration", label: "Max duration", placeholder: undefined },
] as const;

type FilterProps = {
  filters: TraceFilters;
  onChange: (change: Partial<TraceFilters>) => void;
  disabled: boolean;
};

function DatabaseSelect({
  filters,
  onChange,
  disabled,
  inspection,
}: FilterProps & { inspection: ReturnType<typeof useInspection> }) {
  const { database } = filters;
  return (
    <label className="flex min-w-40 flex-1 flex-col gap-1 text-xs text-muted-foreground">
      Database
      <select
        className={INPUT_CLASS}
        value={database}
        onChange={(e) => onChange({ database: e.target.value })}
        disabled={disabled}
      >
        <option value="">Connection default</option>
        {database && !inspection.databases.includes(database) ? (
          <option value={database}>{database}</option>
        ) : null}
        {inspection.databases.map((name) => (
          <option key={name} value={name}>{name}</option>
        ))}
      </select>
      {inspection.loading ? <span>Loading databases…</span> : null}
      {inspection.error ? (
        <span role="alert">Could not load databases. Connection default is still available.</span>
      ) : null}
    </label>
  );
}

/** The trace filter inputs, rendered as siblings so the caller owns the row layout. */
export function TraceFilterFields({
  filters,
  onChange,
  disabled,
  inspection,
}: FilterProps & { inspection: ReturnType<typeof useInspection> }) {
  return (
    <>
      <DatabaseSelect
        filters={filters}
        onChange={onChange}
        disabled={disabled}
        inspection={inspection}
      />
      {LIST_FIELDS.map(({ key, label }) => (
        <label
          key={key}
          className="flex min-w-40 flex-1 flex-col gap-1 text-xs text-muted-foreground"
        >
          {label}
          <input
            className={INPUT_CLASS}
            value={filters[key]}
            onChange={(e) => onChange({ [key]: e.target.value })}
            disabled={disabled}
            placeholder="Comma-separated"
          />
        </label>
      ))}
      {DURATION_FIELDS.map(({ key, label, placeholder }) => (
        <label
          key={key}
          className="flex w-32 flex-col gap-1 text-xs text-muted-foreground"
        >
          {label}
          <input
            className={INPUT_CLASS}
            value={filters[key]}
            onChange={(e) => onChange({ [key]: e.target.value })}
            disabled={disabled}
            placeholder={placeholder}
          />
        </label>
      ))}
    </>
  );
}
