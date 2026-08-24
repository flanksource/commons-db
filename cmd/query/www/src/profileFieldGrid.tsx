import { useState } from "react";
import { IconButton } from "@flanksource/clicky-ui";
import {
  UiDotsVertical,
  UiEye,
  UiEyeClosed,
  UiTrash,
} from "@flanksource/clicky-ui/icons";
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

/** The in-flight row drag, shared by every row so a row can tell whether it is
 *  the one being dragged and which of its edges the drop would land on. */
type ProfileFieldDrag = {
  /** Name of the field being dragged, or "" when no drag is in flight. */
  source: string;
  /** Which edge of `name` an insertion line belongs on, if any. */
  edgeFor: (name: string) => "top" | "bottom" | null;
  start: (name: string) => void;
  over: (name: string) => void;
  drop: (name: string) => void;
  end: () => void;
};

/**
 * Spreadsheet-style column list: include, label and type are edited in place,
 * with quick visibility and delete actions on every row. Anything that does not
 * fit a grid cell — role, format, unit, width, CEL — stays in the inspector pane.
 *
 * Rows are dragged by their handle to reorder the profile's columns. Only
 * configured fields take part: a deleted one has no position of its own, so it
 * is neither draggable nor a drop target.
 */
export function ProfileFieldGrid({ state }: { state: ProfileFieldState }) {
  const [source, setSource] = useState("");
  const [target, setTarget] = useState("");
  const order = state.visibleFields.map((field) => field.name);
  const end = () => {
    setSource("");
    setTarget("");
  };
  const drag: ProfileFieldDrag = {
    source,
    edgeFor: (name) =>
      !source || target !== name || source === name
        ? null
        : order.indexOf(source) < order.indexOf(name)
          ? "bottom"
          : "top",
    start: setSource,
    over: setTarget,
    drop: (name) => {
      state.reorderField(source, name);
      end();
    },
    end,
  };
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
              drag={drag}
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

const dropEdgeClassName = {
  top: "[&>td]:shadow-[inset_0_2px_0_0_var(--color-primary)]",
  bottom: "[&>td]:shadow-[inset_0_-2px_0_0_var(--color-primary)]",
} as const;

function ProfileFieldGridRow({
  field,
  state,
  drag,
  selected,
  active,
}: {
  field: ProfileColumn;
  state: ProfileFieldState;
  drag: ProfileFieldDrag;
  selected: boolean;
  active: boolean;
}) {
  // The row carries text inputs, so it stays undraggable until the handle is
  // pressed — otherwise selecting text in a cell starts a reorder.
  const [grabbed, setGrabbed] = useState(false);
  const fieldState = !selected ? "deleted" : field.hidden ? "hidden" : "visible";
  const stateClassName =
    fieldState === "deleted"
      ? "text-muted-foreground line-through opacity-60 [&_input]:line-through [&_select]:line-through"
      : fieldState === "hidden"
        ? "text-muted-foreground opacity-60"
        : "";
  const dragging = drag.source === field.name;
  const droppable = selected && Boolean(drag.source) && !dragging;
  const edge = droppable ? drag.edgeFor(field.name) : null;
  const release = () => {
    setGrabbed(false);
    drag.end();
  };
  return (
    <tr
      role="listitem"
      data-field-state={fieldState}
      data-drop-edge={edge ?? undefined}
      draggable={grabbed}
      onDragStart={(event) => {
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", field.name);
        drag.start(field.name);
      }}
      onDragOver={(event) => {
        if (!droppable) return;
        event.preventDefault();
        event.dataTransfer.dropEffect = "move";
        drag.over(field.name);
      }}
      onDrop={(event) => {
        if (!droppable) return;
        event.preventDefault();
        setGrabbed(false);
        drag.drop(field.name);
      }}
      onDragEnd={release}
      className={[
        "border-b border-border",
        active ? "bg-primary/10" : "hover:bg-muted/40",
        stateClassName,
        dragging ? "opacity-40" : "",
        edge ? dropEdgeClassName[edge] : "",
      ]
        .filter(Boolean)
        .join(" ")}
      onClick={() => state.setActiveName(field.name)}
    >
      <td className="px-3 py-1" onClick={(event) => event.stopPropagation()}>
        <div className="flex items-center gap-1">
          <IconButton
            icon={UiDotsVertical}
            label={`Reorder ${field.name}`}
            disabled={!selected}
            className="cursor-grab active:cursor-grabbing"
            onMouseDown={() => setGrabbed(true)}
            onMouseUp={release}
            onKeyDown={(event) => {
              const offset =
                event.key === "ArrowUp" ? -1 : event.key === "ArrowDown" ? 1 : 0;
              if (!offset) return;
              event.preventDefault();
              state.moveField(field, offset);
            }}
          />
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
