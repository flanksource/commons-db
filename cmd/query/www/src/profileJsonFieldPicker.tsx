import { Button, Icon } from "@flanksource/clicky-ui";
import { UiColumns } from "@flanksource/clicky-ui/icons";
import { useState, type Dispatch, type SetStateAction } from "react";
import {
  configuredColumnName,
  promotedColumnsError,
  type JsonFieldCandidate,
} from "./profileJsonFields";
import type { ProfileColumn } from "./profileWizardModel";

type ProfileJsonFieldPickerProps = {
  candidates: JsonFieldCandidate[];
  existingColumns: ProfileColumn[];
  saving: boolean;
  error?: string;
  onCancel: () => void;
  onSave: (columns: ProfileColumn[]) => void | Promise<void>;
};

export const promotedColumnTypes = [
  "string",
  "number",
  "boolean",
  "datetime",
  "duration",
  "bytes",
] as const;

export function ProfileJsonFieldPicker({
  candidates,
  existingColumns,
  saving,
  error,
  onCancel,
  onSave,
}: ProfileJsonFieldPickerProps) {
  const [stage, setStage] = useState<"select" | "review">("select");
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [drafts, setDrafts] = useState<ProfileColumn[]>([]);

  const filtered = candidates.filter((candidate) =>
    `${candidate.source} ${candidate.name} ${candidate.sample}`
      .toLowerCase()
      .includes(search.trim().toLowerCase()),
  );
  const validationError = promotedColumnsError(existingColumns, drafts);

  const reviewSelection = () => {
    setDrafts(
      candidates
        .filter((candidate) => selected.has(candidate.id))
        .map((candidate) => ({
          name: candidate.name,
          label: fieldLabel(candidate.name),
          type: candidate.type,
          cel: candidate.cel,
        })),
    );
    setStage("review");
  };

  if (stage === "review") {
    return (
      <div className="flex flex-col gap-4">
        <div>
          <h3 className="text-sm font-semibold">Review columns</h3>
          <p className="mt-1 text-sm text-muted-foreground">
            Adjust the column names, labels, and display types before adding them.
          </p>
        </div>
        <div className="space-y-3">
          {drafts.map((draft, index) => (
            <div key={draft.cel} className="grid gap-3 rounded-md border p-3 md:grid-cols-3">
              <label className="text-xs font-medium text-muted-foreground">
                Column name
                <input
                  className="mt-1 h-9 w-full rounded-md border bg-background px-3 text-sm text-foreground"
                  value={draft.name}
                  onChange={(event) => updateDraft(setDrafts, index, "name", event.target.value)}
                />
              </label>
              <label className="text-xs font-medium text-muted-foreground">
                Label
                <input
                  className="mt-1 h-9 w-full rounded-md border bg-background px-3 text-sm text-foreground"
                  value={draft.label ?? ""}
                  onChange={(event) => updateDraft(setDrafts, index, "label", event.target.value)}
                />
              </label>
              <label className="text-xs font-medium text-muted-foreground">
                Display type
                <select
                  className="mt-1 h-9 w-full rounded-md border bg-background px-3 text-sm text-foreground"
                  value={draft.type ?? "string"}
                  onChange={(event) => updateDraft(setDrafts, index, "type", event.target.value)}
                >
                  {promotedColumnTypes.map((type) => <option key={type}>{type}</option>)}
                </select>
              </label>
            </div>
          ))}
        </div>
        {error || validationError ? (
          <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
            {error ?? validationError}
          </div>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" disabled={saving} onClick={() => setStage("select")}>
            Back
          </Button>
          <Button
            type="button"
            disabled={saving || Boolean(validationError)}
            onClick={() => void onSave(drafts)}
          >
            <Icon icon={UiColumns} className="size-4" />
            {saving ? "Adding…" : `Add ${drafts.length} columns`}
          </Button>
        </div>
      </div>
    );
  }

  const groups = filtered.reduce((result, candidate) => {
    const fields = result.get(candidate.source) ?? [];
    fields.push(candidate);
    result.set(candidate.source, fields);
    return result;
  }, new Map<string, JsonFieldCandidate[]>());
  return (
    <div className="flex flex-col gap-4">
      <div>
        <h3 className="text-sm font-semibold">Add JSON fields</h3>
        <p className="mt-1 text-sm text-muted-foreground">
          Select scalar fields discovered in this row's JSON columns.
        </p>
      </div>
      <input
        aria-label="Search JSON fields"
        placeholder="Search fields"
        className="h-9 w-full rounded-md border bg-background px-3 text-sm text-foreground"
        value={search}
        onChange={(event) => setSearch(event.target.value)}
      />
      <div className="max-h-[55vh] space-y-4 overflow-y-auto pr-1">
        {[...groups.entries()].map(([source, fields]) => (
          <section key={source} className="space-y-2">
            <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">{source}</h4>
            {fields.map((candidate) => {
              const configuredAs = configuredColumnName(candidate, existingColumns);
              return (
                <label key={candidate.id} className="flex items-start gap-3 rounded-md border p-3">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={selected.has(candidate.id)}
                    disabled={configuredAs != null}
                    onChange={(event) => setSelected(toggleSelection(selected, candidate.id, event.target.checked))}
                  />
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
                      {candidate.name}
                      <span className="rounded bg-muted px-1.5 py-0.5 text-xs font-normal text-muted-foreground">
                        {candidate.type}
                      </span>
                      {configuredAs ? (
                        <span className="text-xs font-normal text-muted-foreground">Configured as {configuredAs}</span>
                      ) : null}
                    </span>
                    <span className="block truncate text-xs text-muted-foreground">{String(candidate.sample)}</span>
                  </span>
                </label>
              );
            })}
          </section>
        ))}
        {filtered.length === 0 ? (
          <div className="py-8 text-center text-sm text-muted-foreground">No matching JSON fields</div>
        ) : null}
      </div>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>
        <Button type="button" disabled={selected.size === 0} onClick={reviewSelection}>
          Review selected
        </Button>
      </div>
    </div>
  );
}

function toggleSelection(selected: Set<string>, id: string, checked: boolean): Set<string> {
  const next = new Set(selected);
  if (checked) next.add(id);
  else next.delete(id);
  return next;
}

function updateDraft(
  setDrafts: Dispatch<SetStateAction<ProfileColumn[]>>,
  index: number,
  key: "name" | "label" | "type",
  value: string,
) {
  setDrafts((current) => current.map((draft, offset) =>
    offset === index ? { ...draft, [key]: value } : draft,
  ));
}

function fieldLabel(name: string): string {
  const leaf = name.split(".").at(-1) ?? name;
  return leaf.replace(/_/g, " ").replace(/\b\w/g, (letter: string) => letter.toUpperCase());
}
