import { cn } from "@flanksource/clicky-ui";
import { useMemo, useState, type ReactNode } from "react";
import {
  fieldFormats,
  fieldRoles,
  fieldTypes,
  fieldUnits,
  mockFields,
  previewColumns,
  previewRows,
  sections,
  type MockField,
  type SectionKey,
} from "./mockProfile";

export const inputClass =
  "w-full rounded-md border border-input bg-background px-3 py-2 text-sm outline-none focus:border-primary focus:ring-2 focus:ring-primary/15";

export const inputClassSm =
  "w-full rounded-md border border-input bg-background px-2 py-1 text-xs outline-none focus:border-primary focus:ring-2 focus:ring-primary/15";

/** Shared editing state so every variant manipulates the same fixture. */
export function useFieldState() {
  const [fields, setFields] = useState<MockField[]>(mockFields);
  const [active, setActive] = useState("url.path");
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [selectionFilter, setSelectionFilter] = useState("all");

  const visible = useMemo(
    () =>
      fields.filter((field) => {
        if (query && !field.name.toLowerCase().includes(query.toLowerCase())) return false;
        if (typeFilter && field.type !== typeFilter) return false;
        if (selectionFilter === "selected" && !field.included) return false;
        if (selectionFilter === "unselected" && field.included) return false;
        return true;
      }),
    [fields, query, selectionFilter, typeFilter],
  );

  const activeField = fields.find((field) => field.name === active) ?? visible[0] ?? fields[0];
  const includedCount = fields.filter((field) => field.included).length;

  const patch = (name: string, next: Partial<MockField>) =>
    setFields((current) =>
      current.map((field) => (field.name === name ? { ...field, ...next } : field)),
    );

  const toggle = (name: string, included: boolean) => patch(name, { included });

  const move = (name: string, offset: number) =>
    setFields((current) => {
      const index = current.findIndex((field) => field.name === name);
      const target = index + offset;
      if (index < 0 || target < 0 || target >= current.length) return current;
      const next = [...current];
      [next[index], next[target]] = [next[target], next[index]];
      return next;
    });

  return {
    fields,
    visible,
    active,
    activeField,
    includedCount,
    query,
    typeFilter,
    selectionFilter,
    setActive,
    setQuery,
    setTypeFilter,
    setSelectionFilter,
    patch,
    toggle,
    move,
  };
}

export type FieldState = ReturnType<typeof useFieldState>;

export function TypeBadge({ type }: { type: string }) {
  return (
    <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wide text-muted-foreground">
      {type || "auto"}
    </span>
  );
}

/** Compact field row — name, badges and include checkbox on one line, so a
 * 40rem-wide rail still shows ~20 fields without scrolling. */
export function FieldRow({
  field,
  active,
  onSelect,
  onToggle,
}: {
  field: MockField;
  active: boolean;
  onSelect: () => void;
  onToggle: (included: boolean) => void;
}) {
  return (
    <div
      className={`flex w-full items-center gap-2 border-b border-border px-3 py-1.5 text-sm transition-colors ${
        active ? "bg-primary/10 ring-1 ring-inset ring-primary/30" : "hover:bg-muted/60"
      }`}
    >
      <input
        type="checkbox"
        checked={field.included}
        aria-label={`Include ${field.name}`}
        onChange={(event) => onToggle(event.target.checked)}
      />
      <button
        type="button"
        className="flex min-w-0 flex-1 items-center gap-2 text-left"
        onClick={onSelect}
      >
        <span className={`min-w-0 flex-1 truncate font-mono text-xs ${field.included ? "" : "text-muted-foreground"}`}>
          {field.name}
        </span>
        {field.label ? (
          <span className="shrink-0 truncate text-[11px] text-muted-foreground">{field.label}</span>
        ) : null}
        {field.cel ? (
          <span className="shrink-0 rounded bg-primary/10 px-1 py-0.5 font-mono text-[10px] text-primary">
            cel
          </span>
        ) : null}
        <TypeBadge type={field.type} />
      </button>
    </div>
  );
}

export function LabelledField({
  label,
  help,
  children,
}: {
  label: string;
  help?: string;
  children: ReactNode;
}) {
  return (
    <label className="grid gap-1 text-xs font-medium">
      <span>{label}</span>
      {children}
      {help ? <span className="text-[11px] font-normal text-muted-foreground">{help}</span> : null}
    </label>
  );
}

/** The inspector form. `columns` lets a wide right pane use two columns while
 * a narrow drawer stacks to one. */
export function FieldDetailForm({
  field,
  columns = 2,
  onPatch,
}: {
  field: MockField;
  columns?: 1 | 2;
  onPatch: (next: Partial<MockField>) => void;
}) {
  return (
    <div className={`grid gap-3 ${columns === 2 ? "sm:grid-cols-2" : ""}`}>
      <LabelledField label="Name" help="Unique field name used by filters and expressions.">
        <input value={field.name} className={inputClass} onChange={(event) => onPatch({ name: event.target.value })} />
      </LabelledField>
      <LabelledField label="Display label" help="Blank uses the field name.">
        <input
          value={field.label ?? ""}
          placeholder={field.name}
          className={inputClass}
          onChange={(event) => onPatch({ label: event.target.value || undefined })}
        />
      </LabelledField>
      <LabelledField label="Data type" help="Value shape and default formatting.">
        <select value={field.type} className={inputClass} onChange={(event) => onPatch({ type: event.target.value })}>
          {fieldTypes.map((type) => (
            <option key={type} value={type}>{type}</option>
          ))}
        </select>
      </LabelledField>
      <LabelledField label="Role" help="Table behaviour, independent of Type.">
        <select value={field.role ?? ""} className={inputClass} onChange={(event) => onPatch({ role: event.target.value || undefined })}>
          {fieldRoles.map((role) => (
            <option key={role.value} value={role.value}>{role.label}</option>
          ))}
        </select>
      </LabelledField>
      <LabelledField label="Format" help="Unit takes precedence when both are set.">
        <select value={field.format ?? ""} className={inputClass} onChange={(event) => onPatch({ format: event.target.value || undefined })}>
          {fieldFormats.map((format) => (
            <option key={format.value} value={format.value}>{format.label}</option>
          ))}
        </select>
      </LabelledField>
      <LabelledField label="Unit" help="Numeric scaling for number, duration or bytes.">
        <select value={field.unit ?? ""} className={inputClass} onChange={(event) => onPatch({ unit: event.target.value || undefined })}>
          {fieldUnits.map((unit) => (
            <option key={unit.value} value={unit.value}>{unit.label}</option>
          ))}
        </select>
      </LabelledField>
      <LabelledField label="Max width (characters)" help="Blank uses the table default.">
        <input
          type="number"
          value={field.width ?? ""}
          placeholder="Auto"
          className={inputClass}
          onChange={(event) => onPatch({ width: event.target.value ? Number(event.target.value) : undefined })}
        />
      </LabelledField>
      <label className="flex items-end gap-2 pb-1 text-xs font-medium">
        <input type="checkbox" checked={field.hidden ?? false} onChange={(event) => onPatch({ hidden: event.target.checked })} />
        Hide in the default table
      </label>
      <div className={columns === 2 ? "sm:col-span-2" : ""}>
        <LabelledField label="CEL expression" help="Optional expression computing the value from row.">
          <textarea
            rows={3}
            value={field.cel ?? ""}
            placeholder="Optional value transformation"
            className={`${inputClass} resize-y font-mono text-[11px]`}
            onChange={(event) => onPatch({ cel: event.target.value || undefined })}
          />
        </LabelledField>
      </div>
    </div>
  );
}

/** Filter header shared by every field list. */
export function FieldFilters({ state, compact = false }: { state: FieldState; compact?: boolean }) {
  const types = Array.from(new Set(state.fields.map((field) => field.type))).sort();
  return (
    <div className={`grid gap-2 ${compact ? "grid-cols-[1fr_auto_auto]" : ""}`}>
      <input
        type="search"
        value={state.query}
        placeholder={`Search ${state.fields.length} fields`}
        aria-label="Search fields"
        className={compact ? inputClassSm : inputClass}
        onChange={(event) => state.setQuery(event.target.value)}
      />
      <select
        value={state.typeFilter}
        aria-label="Filter by field type"
        className={compact ? `${inputClassSm} w-auto` : inputClass}
        onChange={(event) => state.setTypeFilter(event.target.value)}
      >
        <option value="">All types</option>
        {types.map((type) => (
          <option key={type} value={type}>{type}</option>
        ))}
      </select>
      <select
        value={state.selectionFilter}
        aria-label="Filter by selection"
        className={compact ? `${inputClassSm} w-auto` : inputClass}
        onChange={(event) => state.setSelectionFilter(event.target.value)}
      >
        <option value="all">All fields</option>
        <option value="selected">Selected</option>
        <option value="unselected">Not selected</option>
      </select>
    </div>
  );
}

/** Vertical section nav used by variants B, C and D in place of top tabs. */
export function SectionRail({
  value,
  onChange,
  className,
}: {
  value: SectionKey;
  onChange: (key: SectionKey) => void;
  className?: string;
}) {
  return (
    <nav
      className={cn("w-56 shrink-0 overflow-auto border-r border-border bg-muted/30 p-2", className)}
      aria-label="Profile sections"
    >
      {sections.map((section) => {
        const active = section.key === value;
        return (
          <button
            key={section.key}
            type="button"
            aria-current={active ? "page" : undefined}
            className={`mb-0.5 flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left transition-colors ${
              active ? "bg-background shadow-sm ring-1 ring-border" : "hover:bg-background/70"
            }`}
            onClick={() => onChange(section.key)}
          >
            <span className="min-w-0 flex-1">
              <span className={`block truncate text-sm ${active ? "font-medium text-foreground" : "text-foreground/80"}`}>
                {section.label}
              </span>
              <span className="block truncate text-[11px] text-muted-foreground">{section.hint}</span>
            </span>
            {section.attention ? (
              <span className="size-1.5 shrink-0 rounded-full bg-warning" title="Needs attention" />
            ) : null}
            {section.badge ? (
              <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                {section.badge}
              </span>
            ) : null}
          </button>
        );
      })}
    </nav>
  );
}

/** Spreadsheet-style field list: include, label, type and width edited in place,
 * so renaming N columns is N clicks instead of N select-then-edit round trips. */
export function FieldGrid({ state }: { state: FieldState }) {
  return (
    <div className="min-h-0 flex-1 overflow-auto">
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
          {state.visible.map((field) => (
            <tr
              key={field.name}
              className={`border-b border-border ${
                state.activeField?.name === field.name ? "bg-primary/10" : "hover:bg-muted/40"
              }`}
              onClick={() => state.setActive(field.name)}
            >
              <td className="px-3 py-1">
                <input
                  type="checkbox"
                  checked={field.included}
                  aria-label={`Include ${field.name}`}
                  onChange={(event) => state.toggle(field.name, event.target.checked)}
                />
              </td>
              <td className="max-w-[18rem] truncate px-3 py-1 font-mono text-xs">{field.name}</td>
              <td className="px-3 py-1">
                <input
                  value={field.label ?? ""}
                  placeholder={field.name}
                  aria-label={`Label for ${field.name}`}
                  className={`${inputClassSm} min-w-[9rem]`}
                  onChange={(event) => state.patch(field.name, { label: event.target.value || undefined })}
                />
              </td>
              <td className="px-3 py-1">
                <select
                  value={field.type}
                  aria-label={`Type for ${field.name}`}
                  className={`${inputClassSm} w-auto`}
                  onChange={(event) => state.patch(field.name, { type: event.target.value })}
                >
                  {fieldTypes.map((type) => (
                    <option key={type} value={type}>{type}</option>
                  ))}
                </select>
              </td>
              <td className="px-3 py-1">
                <input
                  type="number"
                  value={field.width ?? ""}
                  placeholder="Auto"
                  aria-label={`Width for ${field.name}`}
                  className={`${inputClassSm} w-20`}
                  onChange={(event) =>
                    state.patch(field.name, {
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
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Live preview of the configured columns against sampled rows. */
export function PreviewTable({ fields, dense = false }: { fields: MockField[]; dense?: boolean }) {
  const shown = previewColumns.filter((name) =>
    fields.some((field) => field.name === name && field.included && !field.hidden),
  );
  const labelFor = (name: string) => fields.find((field) => field.name === name)?.label ?? name;
  return (
    <div className="min-h-0 overflow-auto">
      <table className="w-full border-collapse text-left">
        <thead className="sticky top-0 bg-muted/60 backdrop-blur">
          <tr>
            {shown.map((name) => (
              <th key={name} className={`whitespace-nowrap border-b border-border px-3 font-medium text-muted-foreground ${dense ? "py-1 text-[11px]" : "py-1.5 text-xs"}`}>
                {labelFor(name)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {previewRows.map((row, index) => (
            <tr key={index} className="hover:bg-muted/40">
              {shown.map((name) => {
                const value = row[name as keyof typeof row];
                const bad = name === "http.response.status_code" && Number(value) >= 500;
                return (
                  <td
                    key={name}
                    className={`whitespace-nowrap border-b border-border px-3 font-mono ${dense ? "py-1 text-[11px]" : "py-1.5 text-xs"} ${bad ? "text-destructive" : ""}`}
                  >
                    {String(value)}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      {shown.length === 0 ? (
        <p className="p-6 text-center text-xs text-muted-foreground">No columns included yet.</p>
      ) : null}
    </div>
  );
}

/** Header actions repeated across the inspector panes. */
export function InspectorActions({
  field,
  onMove,
  onToggle,
}: {
  field: MockField;
  onMove: (offset: number) => void;
  onToggle: (included: boolean) => void;
}) {
  return (
    <div className="flex items-center gap-1">
      <button type="button" className="rounded px-2 py-1 text-xs text-muted-foreground hover:bg-muted hover:text-foreground" onClick={() => onMove(-1)}>
        Move up
      </button>
      <button type="button" className="rounded px-2 py-1 text-xs text-muted-foreground hover:bg-muted hover:text-foreground" onClick={() => onMove(1)}>
        Move down
      </button>
      <label className="ml-1 flex items-center gap-1.5 rounded px-2 py-1 text-xs font-medium">
        <input type="checkbox" checked={field.included} onChange={(event) => onToggle(event.target.checked)} />
        Include
      </label>
    </div>
  );
}
