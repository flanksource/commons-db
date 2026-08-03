/**
 * Multi-field sort. The order of the entries is the tie-break order the backend
 * applies, so moving an entry is an edit in its own right rather than cosmetic.
 */

import { Button, Combobox, IconButton, InputField, Select } from "@flanksource/clicky-ui";
import { UiAdd, UiArrowDown, UiArrowUp, UiTrash } from "@flanksource/clicky-ui/icons";
import type { EsSortBy } from "./esQueryBuilderModel";
import { sortableFields, type EsFieldMapping } from "./esQueryOperators";

/** moveSortEntry shifts one entry, clamping at the ends rather than wrapping. */
export function moveSortEntry(
  sort: EsSortBy[],
  index: number,
  delta: number,
): EsSortBy[] {
  const target = index + delta;
  if (target < 0 || target >= sort.length) return sort;
  const moved = [...sort];
  const [entry] = moved.splice(index, 1);
  moved.splice(target, 0, entry);
  return moved;
}

export function EsQuerySortEditor({
  sort,
  fields,
  orders,
  onChange,
}: {
  sort: EsSortBy[];
  fields: EsFieldMapping[];
  orders: string[];
  onChange: (sort: EsSortBy[]) => void;
}) {
  const options = sortableFields(fields).map((name) => ({
    value: name,
    label: name,
  }));
  const set = (index: number, patch: Partial<EsSortBy>) =>
    onChange(
      sort.map((entry, position) =>
        position === index ? { ...entry, ...patch } : entry,
      ),
    );
  const text = (value: string | undefined) => value ?? "";

  return (
    <section className="flex flex-col gap-2">
      <header className="flex items-center gap-2">
        <h3 className="text-xs font-medium text-muted-foreground">Sort</h3>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => onChange([...sort, { field: "", order: orders[0] }])}
        >
          <UiAdd className="text-xs" /> Sort field
        </Button>
      </header>
      {sort.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          Unsorted — hits come back in the backend's own order.
        </p>
      ) : null}
      {sort.map((entry, index) => (
        <div key={index} className="flex flex-wrap items-center gap-2">
          <Combobox
            ariaLabel="Sort field"
            className="min-w-48 flex-1"
            value={entry.field}
            onChange={(next) => set(index, { field: next })}
            options={options}
            placeholder="Field…"
            allowCustomValue
          />
          <span className="w-28 shrink-0">
            <Select
              aria-label="Sort order"
              value={text(entry.order)}
              options={[
                { value: "", label: "order" },
                ...orders.map((order) => ({ value: order, label: order })),
              ]}
              onChange={(event) =>
                set(index, { order: event.target.value || undefined })
              }
            />
          </span>
          <InputField
            aria-label="Sort mode"
            className="w-28"
            placeholder="mode"
            title="How a multi-valued field is reduced: min, max, sum, avg, median"
            value={text(entry.mode)}
            onChange={(next) => set(index, { mode: next || undefined })}
          />
          <InputField
            aria-label="Missing"
            className="w-28"
            placeholder="missing"
            title="Where documents without the field sort: _first, _last, or a substitute value"
            value={text(entry.missing)}
            onChange={(next) => set(index, { missing: next || undefined })}
          />
          <InputField
            aria-label="Unmapped type"
            className="w-32"
            placeholder="unmapped type"
            title="Treat the field as this type where it is unmapped"
            value={text(entry.unmappedType)}
            onChange={(next) => set(index, { unmappedType: next || undefined })}
          />
          <IconButton
            icon={UiArrowUp}
            label={`Move ${entry.field || "sort field"} earlier`}
            disabled={index === 0}
            onClick={() => onChange(moveSortEntry(sort, index, -1))}
          />
          <IconButton
            icon={UiArrowDown}
            label={`Move ${entry.field || "sort field"} later`}
            disabled={index === sort.length - 1}
            onClick={() => onChange(moveSortEntry(sort, index, 1))}
          />
          <IconButton
            icon={UiTrash}
            label={`Remove ${entry.field || "sort field"}`}
            onClick={() =>
              onChange(sort.filter((_entry, position) => position !== index))
            }
          />
        </div>
      ))}
    </section>
  );
}
