import { IconButton } from "@flanksource/clicky-ui";
import { UiEye, UiEyeClosed, UiTrash } from "@flanksource/clicky-ui/icons";
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
 * Spreadsheet-style column list: include, label and type are edited in place,
 * with quick visibility and delete actions on every row. Anything that does not
 * fit a grid cell — role, format, unit, width, CEL — stays in the inspector pane.
 */
export function ProfileFieldGrid({ state }: { state: ProfileFieldState }) {
  return (
    <div className="min-h-0 flex-1 overflow-auto" role="list">
      <table className="w-fit border-collapse text-left">
        <thead className="sticky top-0 z-10 bg-muted/70 backdrop-blur">
          <tr>
            {["Actions", "Field", "Display label", "Type", "CEL"].map((heading) => (
              <th
                key={heading}
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
  const fieldState = !selected ? "deleted" : field.hidden ? "hidden" : "visible";
  const stateClassName =
    fieldState === "deleted"
      ? "text-muted-foreground line-through opacity-60 [&_input]:line-through [&_select]:line-through"
      : fieldState === "hidden"
        ? "text-muted-foreground opacity-60"
        : "";
  return (
    <tr
      role="listitem"
      data-field-state={fieldState}
      className={`border-b border-border ${active ? "bg-primary/10" : "hover:bg-muted/40"} ${stateClassName}`}
      onClick={() => state.setActiveName(field.name)}
    >
      <td className="px-3 py-1" onClick={(event) => event.stopPropagation()}>
        <div className="flex items-center gap-1">
          <IconButton
            icon={field.hidden ? UiEye : UiEyeClosed}
            label={`${field.hidden ? "Show" : "Hide"} ${field.name}`}
            disabled={!selected}
            onClick={() => state.patchField(field, { hidden: !field.hidden })}
          />
          <IconButton
            icon={UiTrash}
            label={`Delete ${field.name}`}
            disabled={!selected}
            className="text-destructive hover:text-destructive"
            onClick={() => state.removeField(field)}
          />
        </div>
      </td>
      <td className="px-3 py-1">
        <input
          value={field.name}
          aria-label={`Rename ${field.name}`}
          disabled={!selected}
          className={`${inputClassNameSm} min-w-[10rem] font-mono`}
          onChange={(event) =>
            state.patchField(field, { name: event.target.value })
          }
        />
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
