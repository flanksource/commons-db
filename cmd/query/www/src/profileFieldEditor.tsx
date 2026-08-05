import { Button } from "@flanksource/clicky-ui";
import type { ReactNode } from "react";
import {
  PROFILE_COLUMN_FORMAT_OPTIONS,
  PROFILE_COLUMN_UNIT_OPTIONS,
  type ProfileColumn,
} from "./profileWizardModel";

const inputClassName =
  "w-full rounded-md border border-input bg-background px-3 py-2 text-sm outline-none focus:border-primary focus:ring-2 focus:ring-primary/15";

type FieldActions = {
  selected: boolean;
  canMoveUp: boolean;
  canMoveDown: boolean;
  onSelectedChange: (selected: boolean) => void;
  onMoveUp: () => void;
  onMoveDown: () => void;
  onRemove: () => void;
};

export const profileFieldEditorEmptyMessage =
  "Add a column or run a sample to discover fields.";

/** Ordering, removal and inclusion — the operations that act on the field as a
 *  whole rather than on one of its properties. */
export function ProfileFieldEditorActions({
  canMoveUp,
  canMoveDown,
  selected,
  onSelectedChange,
  onMoveUp,
  onMoveDown,
  onRemove,
}: FieldActions) {
  return (
    <>
      <Button type="button" size="sm" variant="ghost" disabled={!canMoveUp} onClick={onMoveUp}>
        Move up
      </Button>
      <Button type="button" size="sm" variant="ghost" disabled={!canMoveDown} onClick={onMoveDown}>
        Move down
      </Button>
      <Button type="button" size="sm" variant="ghost" onClick={onRemove}>
        Remove
      </Button>
      <label className="flex items-center gap-2 text-sm font-medium">
        <input
          type="checkbox"
          checked={selected}
          onChange={(event) => onSelectedChange(event.target.checked)}
        />
        Include
      </label>
    </>
  );
}

/**
 * Everything about a column that a grid cell cannot hold: role, format, unit
 * and the CEL expression. `columns` is explicit rather than a `sm:` breakpoint
 * because the editor route mounts this in a ~380px pane on a wide viewport,
 * where viewport-based breakpoints would wrongly go two-up.
 */
export function ProfileFieldEditorForm({
  field,
  columns = 2,
  onChange,
}: {
  field: ProfileColumn;
  columns?: 1 | 2;
  onChange: (patch: Partial<ProfileColumn>) => void;
}) {
  const wide = columns === 2 ? "sm:col-span-2" : "";
  return (
    <div className={`grid gap-4 ${columns === 2 ? "sm:grid-cols-2" : ""}`}>
      <EditorField label="Output name" help="Public field name used by tables, filters, APIs, and every export.">
        <input value={field.name} className={inputClassName} onChange={(event) => onChange({ name: event.target.value })} />
      </EditorField>
      <EditorField label="Display label" help="Optional table header; blank uses the field name.">
        <input value={field.label ?? ""} className={inputClassName} placeholder={field.name} onChange={(event) => onChange({ label: event.target.value || undefined })} />
      </EditorField>
      <EditorField label="Data type" help="Value shape and default formatting; independent of Role.">
        <select value={field.type ?? ""} className={inputClassName} onChange={(event) => onChange({ type: event.target.value || undefined })}>
          <option value="">Auto detect</option>
          {["string", "number", "boolean", "datetime", "duration", "bytes", "status", "health", "key_value", "key_values", "json"].map((type) => (
            <option key={type} value={type}>{type}</option>
          ))}
        </select>
      </EditorField>
      <EditorField label="Role" help="Optional table behavior, independent of Type.">
        <select value={field.kind ?? ""} className={inputClassName} onChange={(event) => onChange({ kind: event.target.value || undefined })}>
          <option value="">Standard field</option>
          <option value="timestamp">Timestamp</option>
          <option value="tags">Tags</option>
          <option value="status">Status</option>
        </select>
      </EditorField>
      <EditorField label="Format" help="Optional formatter; Unit takes precedence when both are set.">
        <select value={field.format ?? ""} className={inputClassName} onChange={(event) => onChange({ format: event.target.value || undefined })}>
          <option value="">From Type</option>
          {PROFILE_COLUMN_FORMAT_OPTIONS.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
        </select>
      </EditorField>
      <EditorField label="Unit" help="Numeric scaling for number, duration, or bytes Types.">
        <select value={field.unit ?? ""} className={inputClassName} onChange={(event) => onChange({ unit: event.target.value || undefined })}>
          <option value="">No unit</option>
          {PROFILE_COLUMN_UNIT_OPTIONS.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
        </select>
      </EditorField>
      <EditorField label="Max width (characters)" help="Maximum rendered width; blank uses the table default.">
        <input type="number" min={1} value={field.width ?? ""} className={inputClassName} placeholder="Auto" onChange={(event) => onChange({ width: event.target.value ? Number(event.target.value) : undefined })} />
      </EditorField>
      <div className={wide}>
        <EditorField label="CEL expression" help="Optional expression computing the value from row.">
          <textarea rows={4} value={field.cel ?? ""} className={`${inputClassName} resize-y font-mono text-xs`} placeholder="Optional value transformation" onChange={(event) => onChange({ cel: event.target.value || undefined })} />
        </EditorField>
      </div>
      <label className={`flex items-center gap-2 text-sm font-medium ${wide}`}>
        <input type="checkbox" checked={field.hidden ?? false} onChange={(event) => onChange({ hidden: event.target.checked })} />
        Hide this field in the default table
      </label>
    </div>
  );
}

/** The wizard's carded inspector: names the field itself, because its field
 *  list has no header to do it. The editor route composes the parts instead,
 *  since its Workspace pane header already carries the name. */
export function ProfileFieldEditor({
  field,
  onChange,
  ...actions
}: FieldActions & {
  field?: ProfileColumn;
  onChange: (patch: Partial<ProfileColumn>) => void;
}) {
  if (!field) {
    return (
      <section className="grid min-h-64 place-items-center rounded-xl border border-dashed text-sm text-muted-foreground">
        {profileFieldEditorEmptyMessage}
      </section>
    );
  }

  return (
    <section className="overflow-auto rounded-xl border bg-card p-5">
      <div className="mb-5 flex flex-wrap items-start gap-2 border-b pb-4">
        <div className="mr-auto min-w-0">
          <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Field editor
          </p>
          <h3 className="mt-1 truncate font-mono text-sm font-semibold">
            {field.name}
          </h3>
        </div>
        <ProfileFieldEditorActions {...actions} />
      </div>
      <ProfileFieldEditorForm field={field} onChange={onChange} />
    </section>
  );
}

function EditorField({ label, help, children }: { label: string; help?: string; children: ReactNode }) {
  return (
    <label className="grid gap-1.5 text-sm font-medium">
      <span>{label}</span>
      {children}
      {help ? <span className="text-xs font-normal text-muted-foreground">{help}</span> : null}
    </label>
  );
}
