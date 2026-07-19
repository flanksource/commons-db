import { useMemo, useState, type ReactNode } from "react";
import {
  applyVisibleFieldSelection,
  filterProfileFields,
  patchProfileField,
  type ProfileColumn,
  type ProfileFieldFilter,
} from "./profileWizardModel";

type ProfileFieldManagerProps = {
  discovered: ProfileColumn[];
  configured: ProfileColumn[];
  activeName: string;
  onConfiguredChange: (columns: ProfileColumn[]) => void;
  onActiveNameChange: (name: string) => void;
};

const inputClassName =
  "w-full rounded-md border border-input bg-background px-3 py-2 text-sm outline-none focus:border-primary focus:ring-2 focus:ring-primary/15";

export function ProfileFieldManager({
  discovered,
  configured,
  activeName,
  onConfiguredChange,
  onActiveNameChange,
}: ProfileFieldManagerProps) {
  const [filter, setFilter] = useState<ProfileFieldFilter>({
    query: "",
    type: "",
    selection: "all",
  });
  const selectedNames = useMemo(
    () => new Set(configured.map((field) => field.name)),
    [configured],
  );
  const visibleFields = useMemo(
    () => filterProfileFields(discovered, selectedNames, filter),
    [discovered, filter, selectedNames],
  );
  const types = useMemo(
    () =>
      Array.from(
        new Set(discovered.map((field) => field.type).filter(Boolean)),
      ).sort() as string[],
    [discovered],
  );
  const activeField =
    configured.find((field) => field.name === activeName) ??
    discovered.find((field) => field.name === activeName) ??
    configured[0] ??
    discovered[0];

  const setVisibleSelection = (selected: boolean) => {
    onConfiguredChange(
      applyVisibleFieldSelection(
        discovered,
        configured,
        new Set(visibleFields.map((field) => field.name)),
        selected,
      ),
    );
  };

  const setFieldSelection = (field: ProfileColumn, selected: boolean) => {
    onConfiguredChange(
      applyVisibleFieldSelection(
        discovered,
        configured,
        new Set([field.name]),
        selected,
      ),
    );
  };

  const updateActiveField = (patch: Partial<ProfileColumn>) => {
    if (!activeField) return;
    const updated = patchProfileField(activeField, patch);
    const exists = configured.some((field) => field.name === activeField.name);
    onConfiguredChange(
      exists
        ? configured.map((field) =>
            field.name === activeField.name ? updated : field,
          )
        : [...configured, updated],
    );
  };

  return (
    <div className="grid min-h-0 gap-4 lg:grid-cols-[minmax(22rem,0.9fr)_minmax(24rem,1.1fr)]">
      <section className="flex min-h-0 flex-col overflow-hidden rounded-xl border bg-card">
        <div className="space-y-3 border-b p-4">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h3 className="font-semibold">Fields</h3>
              <p className="text-xs text-muted-foreground">
                {configured.length} of {discovered.length} fields selected
              </p>
            </div>
            <div className="flex gap-2 text-xs">
              <button
                type="button"
                className="font-medium text-primary hover:underline"
                onClick={() => setVisibleSelection(true)}
              >
                Select visible
              </button>
              <span className="text-border">|</span>
              <button
                type="button"
                className="font-medium text-muted-foreground hover:text-foreground"
                onClick={() => setVisibleSelection(false)}
              >
                Clear visible
              </button>
            </div>
          </div>
          <input
            type="search"
            value={filter.query}
            onChange={(event) =>
              setFilter((current) => ({
                ...current,
                query: event.target.value,
              }))
            }
            placeholder={`Search ${discovered.length} fields`}
            aria-label="Search fields"
            className={inputClassName}
          />
          <div className="grid grid-cols-2 gap-2">
            <select
              value={filter.type}
              aria-label="Filter by field type"
              className={inputClassName}
              onChange={(event) =>
                setFilter((current) => ({
                  ...current,
                  type: event.target.value,
                }))
              }
            >
              <option value="">All types</option>
              {types.map((type) => (
                <option key={type} value={type}>
                  {type}
                </option>
              ))}
            </select>
            <select
              value={filter.selection}
              aria-label="Filter by selection"
              className={inputClassName}
              onChange={(event) =>
                setFilter((current) => ({
                  ...current,
                  selection: event.target.value as ProfileFieldFilter["selection"],
                }))
              }
            >
              <option value="all">All fields</option>
              <option value="selected">Selected</option>
              <option value="unselected">Not selected</option>
            </select>
          </div>
        </div>
        <div className="min-h-64 flex-1 overflow-auto" role="list">
          {visibleFields.map((field) => {
            const selected = selectedNames.has(field.name);
            const active = activeField?.name === field.name;
            return (
              <div
                key={field.name}
                role="listitem"
                className={`flex w-full items-center gap-3 border-b px-4 py-2.5 text-sm transition-colors ${active ? "bg-primary/8" : "hover:bg-muted/50"}`}
              >
                <input
                  type="checkbox"
                  checked={selected}
                  aria-label={`Include ${field.name}`}
                  onChange={(event) =>
                    setFieldSelection(field, event.target.checked)
                  }
                />
                <button
                  type="button"
                  className="flex min-w-0 flex-1 items-center gap-3 text-left"
                  onClick={() => onActiveNameChange(field.name)}
                >
                  <span className="min-w-0 flex-1 truncate font-mono text-xs">
                    {field.name}
                  </span>
                  <span className="rounded bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">
                    {field.type || "auto"}
                  </span>
                </button>
              </div>
            );
          })}
          {visibleFields.length === 0 ? (
            <p className="p-8 text-center text-sm text-muted-foreground">
              No fields match these filters.
            </p>
          ) : null}
        </div>
      </section>

      <FieldEditor
        field={activeField}
        selected={Boolean(activeField && selectedNames.has(activeField.name))}
        onSelectedChange={(selected) => {
          if (activeField) setFieldSelection(activeField, selected);
        }}
        onChange={updateActiveField}
      />
    </div>
  );
}

function FieldEditor({
  field,
  selected,
  onSelectedChange,
  onChange,
}: {
  field?: ProfileColumn;
  selected: boolean;
  onSelectedChange: (selected: boolean) => void;
  onChange: (patch: Partial<ProfileColumn>) => void;
}) {
  if (!field) {
    return (
      <section className="grid min-h-64 place-items-center rounded-xl border border-dashed text-sm text-muted-foreground">
        Run a sample to discover and configure fields.
      </section>
    );
  }

  return (
    <section className="overflow-auto rounded-xl border bg-card p-5">
      <div className="mb-5 flex items-start justify-between gap-4 border-b pb-4">
        <div className="min-w-0">
          <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Field editor
          </p>
          <h3 className="mt-1 truncate font-mono text-sm font-semibold">
            {field.name}
          </h3>
        </div>
        <label className="flex items-center gap-2 text-sm font-medium">
          <input
            type="checkbox"
            checked={selected}
            onChange={(event) => onSelectedChange(event.target.checked)}
          />
          Include
        </label>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <EditorField label="Display label">
          <input
            value={field.label ?? ""}
            className={inputClassName}
            placeholder={field.name}
            onChange={(event) => onChange({ label: event.target.value || undefined })}
          />
        </EditorField>
        <EditorField label="Data type">
          <select
            value={field.type ?? ""}
            className={inputClassName}
            onChange={(event) => onChange({ type: event.target.value || undefined })}
          >
            <option value="">Auto detect</option>
            {[
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
            ].map((type) => (
              <option key={type} value={type}>
                {type}
              </option>
            ))}
          </select>
        </EditorField>
        <EditorField label="Role">
          <select
            value={field.kind ?? ""}
            className={inputClassName}
            onChange={(event) => onChange({ kind: event.target.value || undefined })}
          >
            <option value="">Standard field</option>
            <option value="timestamp">Timestamp</option>
            <option value="tags">Tags</option>
            <option value="status">Status</option>
          </select>
        </EditorField>
        <EditorField label="Format">
          <input
            value={field.format ?? ""}
            className={inputClassName}
            placeholder="e.g. 0,0.00"
            onChange={(event) => onChange({ format: event.target.value || undefined })}
          />
        </EditorField>
        <EditorField label="Unit">
          <input
            value={field.unit ?? ""}
            className={inputClassName}
            placeholder="e.g. ms, bytes, %"
            onChange={(event) => onChange({ unit: event.target.value || undefined })}
          />
        </EditorField>
        <EditorField label="Width (px)">
          <input
            type="number"
            min={40}
            value={field.width ?? ""}
            className={inputClassName}
            placeholder="Auto"
            onChange={(event) =>
              onChange({
                width: event.target.value
                  ? Number(event.target.value)
                  : undefined,
              })
            }
          />
        </EditorField>
        <div className="sm:col-span-2">
          <EditorField label="CEL expression">
            <textarea
              rows={4}
              value={field.cel ?? ""}
              className={`${inputClassName} resize-y font-mono text-xs`}
              placeholder="Optional value transformation"
              onChange={(event) => onChange({ cel: event.target.value || undefined })}
            />
          </EditorField>
        </div>
        <label className="flex items-center gap-2 text-sm font-medium sm:col-span-2">
          <input
            type="checkbox"
            checked={field.hidden ?? false}
            onChange={(event) => onChange({ hidden: event.target.checked })}
          />
          Hide this field in the default table
        </label>
      </div>
    </section>
  );
}

function EditorField({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <label className="grid gap-1.5 text-sm font-medium">
      <span>{label}</span>
      {children}
    </label>
  );
}
