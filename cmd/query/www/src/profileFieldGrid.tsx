import type { ProfileFieldState } from "./profileFieldState";
import type { ProfileColumn, ProfileFieldFilter } from "./profileWizardModel";

const inputClassName =
  "w-full rounded-md border border-input bg-background px-3 py-2 text-sm outline-none focus:border-primary focus:ring-2 focus:ring-primary/15";

const inputClassNameSm =
  "w-full rounded-md border border-input bg-background px-2 py-1 text-xs outline-none focus:border-primary focus:ring-2 focus:ring-primary/15";

export const PROFILE_FIELD_TYPES = [
  "string",
  "number",
  "boolean",
  "datetime",
  "duration",
  "bytes",
  "status",
  "health",
  "key_value",
  "key_values",
  "json",
] as const;

/** Search, type and selection filters. `compact` lays them out on one row for
 * the editor route's pinned toolbar; the wizard stacks them in its card. */
export function ProfileFieldFilters({
  state,
  compact = false,
}: {
  state: ProfileFieldState;
  compact?: boolean;
}) {
  const className = compact ? inputClassNameSm : inputClassName;
  const patch = (next: Partial<ProfileFieldFilter>) =>
    state.setFilter((current) => ({ ...current, ...next }));
  const search = (
    <input
      type="search"
      value={state.filter.query}
      onChange={(event) => patch({ query: event.target.value })}
      placeholder={`Search ${state.available.length} fields`}
      aria-label="Search fields"
      className={className}
    />
  );
  const selects = (
    <>
      <select
        value={state.filter.type}
        aria-label="Filter by field type"
        className={compact ? `${className} w-auto` : className}
        onChange={(event) => patch({ type: event.target.value })}
      >
        <option value="">All types</option>
        {state.types.map((type) => (
          <option key={type} value={type}>{type}</option>
        ))}
      </select>
      <select
        value={state.filter.selection}
        aria-label="Filter by selection"
        className={compact ? `${className} w-auto` : className}
        onChange={(event) =>
          patch({ selection: event.target.value as ProfileFieldFilter["selection"] })
        }
      >
        <option value="all">All fields</option>
        <option value="selected">Selected</option>
        <option value="unselected">Not selected</option>
      </select>
    </>
  );
  if (compact) {
    return <div className="grid grid-cols-[1fr_auto_auto] gap-2">{search}{selects}</div>;
  }
  return (
    <div className="space-y-3">
      {search}
      <div className="grid grid-cols-2 gap-2">{selects}</div>
    </div>
  );
}

/**
 * Spreadsheet-style column list: include, label, type and width are edited in
 * place, so renaming N columns costs N clicks instead of N select-then-edit
 * round trips through the inspector. Anything that does not fit a grid cell —
 * role, format, unit, CEL — stays in the inspector pane.
 */
export function ProfileFieldGrid({ state }: { state: ProfileFieldState }) {
  return (
    <div className="min-h-0 flex-1 overflow-auto" role="list">
      <table className="w-full border-collapse text-left">
        <thead className="sticky top-0 z-10 bg-muted/70 backdrop-blur">
          <tr>
            {["", "Field", "Display label", "Type", "Width", "CEL"].map((heading, index) => (
              <th
                key={heading || index}
                className="whitespace-nowrap border-b border-border px-3 py-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground"
              >
                {heading}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {state.visibleFields.map((field) => (
            <ProfileFieldGridRow
              key={field.name}
              field={field}
              state={state}
              selected={state.selectedNames.has(field.name)}
              active={state.activeField?.name === field.name}
            />
          ))}
        </tbody>
      </table>
      {state.visibleFields.length === 0 ? (
        <p className="p-8 text-center text-sm text-muted-foreground">
          No fields match these filters.
        </p>
      ) : null}
    </div>
  );
}

function ProfileFieldGridRow({
  field,
  state,
  selected,
  active,
}: {
  field: ProfileColumn;
  state: ProfileFieldState;
  selected: boolean;
  active: boolean;
}) {
  return (
    <tr
      role="listitem"
      className={`border-b border-border ${active ? "bg-primary/10" : "hover:bg-muted/40"}`}
      onClick={() => state.setActiveName(field.name)}
    >
      <td className="px-3 py-1">
        <input
          type="checkbox"
          checked={selected}
          aria-label={`Include ${field.name}`}
          onChange={(event) => state.setFieldSelection(field, event.target.checked)}
        />
      </td>
      <td className="max-w-[18rem] truncate px-3 py-1 font-mono text-xs">
        {field.name}
      </td>
      <td className="px-3 py-1">
        <input
          value={field.label ?? ""}
          placeholder={field.name}
          aria-label={`Label for ${field.name}`}
          className={`${inputClassNameSm} min-w-[9rem]`}
          onChange={(event) =>
            state.patchField(field, { label: event.target.value || undefined })
          }
        />
      </td>
      <td className="px-3 py-1">
        <select
          value={field.type ?? ""}
          aria-label={`Type for ${field.name}`}
          className={`${inputClassNameSm} w-auto`}
          onChange={(event) =>
            state.patchField(field, { type: event.target.value || undefined })
          }
        >
          <option value="">auto</option>
          {PROFILE_FIELD_TYPES.map((type) => (
            <option key={type} value={type}>{type}</option>
          ))}
        </select>
      </td>
      <td className="px-3 py-1">
        <input
          type="number"
          min={1}
          value={field.width ?? ""}
          placeholder="Auto"
          aria-label={`Width for ${field.name}`}
          className={`${inputClassNameSm} w-20`}
          onChange={(event) =>
            state.patchField(field, {
              width: event.target.value ? Number(event.target.value) : undefined,
            })
          }
        />
      </td>
      <td className="px-3 py-1">
        {field.cel ? (
          <span className="rounded bg-primary/10 px-1.5 py-0.5 font-mono text-[10px] text-primary">
            set
          </span>
        ) : (
          <span className="text-[11px] text-muted-foreground">—</span>
        )}
      </td>
    </tr>
  );
}
