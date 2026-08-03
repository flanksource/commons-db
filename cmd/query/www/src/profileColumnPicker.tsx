import { Icon } from "@flanksource/clicky-ui";
import { UiSqlColumn } from "@flanksource/clicky-ui/icons";

export type ProfileColumn = {
  name: string;
  type?: string;
  kind?: string;
  [key: string]: unknown;
};

const PROFILE_COLUMN_TYPE_LABELS: Record<string, string> = {
  key_value: "KeyValue{}",
  key_values: "[]KeyValue",
  json: "JSON",
};

export function profileColumnTypeLabel(type?: string) {
  return type ? (PROFILE_COLUMN_TYPE_LABELS[type] ?? type) : "string";
}

export function ColumnPicker({
  columns,
  selected,
  existing,
  onChange,
  timestampColumn,
  onTimestampColumnChange,
}: {
  columns: ProfileColumn[];
  selected: Set<string>;
  existing: Set<string>;
  onChange: (next: Set<string>) => void;
  timestampColumn: string;
  onTimestampColumnChange: (next: string) => void;
}) {
  return (
    <div className="shrink-0 rounded-md border bg-card p-3">
      <div className="mb-2 flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        <Icon icon={UiSqlColumn} className="size-4" />
        Columns from sample
      </div>
      <div className="flex max-h-32 flex-wrap gap-2 overflow-auto">
        {columns.map((column) => (
          <div
            key={column.name}
            className="flex cursor-pointer items-center gap-2 rounded-md border bg-background px-2 py-1.5 text-xs"
          >
            <label className="flex cursor-pointer items-center gap-2">
              <input
                type="checkbox"
                checked={selected.has(column.name)}
                onChange={(event) => {
                  const next = new Set(selected);
                  if (event.target.checked) next.add(column.name);
                  else {
                    next.delete(column.name);
                    if (timestampColumn === column.name)
                      onTimestampColumnChange("");
                  }
                  onChange(next);
                }}
              />
              <span className="font-medium">{column.name}</span>
              <span className="text-muted-foreground">
                {profileColumnTypeLabel(column.type)}
              </span>
            </label>
            <label className="ml-1 flex cursor-pointer items-center gap-1 border-l pl-2 text-muted-foreground">
              <input
                type="radio"
                name="profile-timestamp-column"
                aria-label={`Use ${column.name} for time ranges`}
                checked={timestampColumn === column.name}
                disabled={!selected.has(column.name)}
                onChange={() => onTimestampColumnChange(column.name)}
              />
              time range
            </label>
            {existing.has(column.name) ? (
              <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                configured
              </span>
            ) : null}
          </div>
        ))}
      </div>
    </div>
  );
}

/**
 * mapTimestampColumn marks the chosen column as the profile's time range and
 * clears the mark from whichever column previously held it.
 */
export function mapTimestampColumn(
  columns: ProfileColumn[],
  timestampColumn: string,
): ProfileColumn[] {
  return columns.map((column) => {
    if (column.name === timestampColumn) {
      return { ...column, type: "datetime", kind: "timestamp" };
    }
    if (column.kind !== "timestamp") return column;
    const { kind: _kind, ...rest } = column;
    return rest;
  });
}
