/**
 * The mapping bench: both profiles' fields on screen while the join key is built
 * from them.
 *
 * A reconcile key is the whole game and is invisible until the run — a wrong one
 * produces a tidy-looking result where every record is missing on both sides.
 * So the two schemas stay visible, the key is assembled by pairing a field on
 * each side, and the CEL that generates is shown rather than hidden. Editing the
 * expression by hand takes it over completely, which is the engine's
 * columns-or-CEL rule expressed as an interaction instead of a validation error.
 */

import { useState } from "react";
import { Badge, Button, Icon, InputField, SegmentedControl, cn } from "@flanksource/clicky-ui";
import { UiAdd, UiArrowRight, UiClock, UiCode2, UiPlay, UiTrash } from "@flanksource/clicky-ui/icons";

import { ProfilePicker } from "./profilePicker";
import {
  celForPairings,
  profileFields,
  routeParams,
  timestampField,
  type KeyPairing,
  type ProfileDocument,
} from "./reconcileModel";

const KEY_MODES = [
  { id: "mapped", label: "Mapped fields" },
  { id: "cel", label: "CEL" },
] as const;

export type KeyMode = (typeof KEY_MODES)[number]["id"];

export type BenchState = {
  dest: string;
  pairings: KeyPairing[];
  mode: KeyMode;
  cel: string;
  limit: number;
  params: Record<string, string>;
};

export function ReconcileBench({
  state,
  onChange,
  source,
  dest,
  destNames,
  onRun,
  onSave,
  running,
  saving,
  error,
}: {
  state: BenchState;
  onChange: (next: BenchState) => void;
  source: ProfileDocument | undefined;
  dest: ProfileDocument | undefined;
  destNames: string[];
  onRun: () => void;
  onSave: () => void;
  running: boolean;
  saving: boolean;
  error: string;
}) {
  const [sourceField, setSourceField] = useState<string>();
  const [destField, setDestField] = useState<string>();

  const cel = state.mode === "cel" ? state.cel : celForPairings(state.pairings);
  const canPair = Boolean(sourceField && destField);
  const routed = routeParams(state.params, source, dest);

  const addPairing = () => {
    if (!sourceField || !destField) return;
    onChange({ ...state, pairings: [...state.pairings, { source: sourceField, dest: destField }] });
    setSourceField(undefined);
    setDestField(undefined);
  };

  return (
    <div className="space-y-3">
      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)_minmax(0,1fr)]">
        <section className="rounded-lg border border-border p-3">
          <SideHeader side="source" name={source?.profile ?? ""} />
          <FieldList
            fields={profileFields(source)}
            selected={sourceField}
            onSelect={setSourceField}
            clock={timestampField(source)}
          />
        </section>

        <section className="space-y-2 rounded-lg border border-primary/30 bg-primary/[0.03] p-3">
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold">Join key</h3>
            <SegmentedControl
              value={state.mode}
              options={KEY_MODES.map((option) => ({ id: option.id, label: option.label }))}
              onChange={(next) =>
                onChange({ ...state, mode: next as KeyMode, cel: next === "cel" ? cel : state.cel })
              }
              size="sm"
              aria-label="Key mode"
            />
          </div>

          {state.mode === "mapped" ? (
            <>
              {state.pairings.map((pairing, index) => (
                <div
                  key={`${pairing.source}-${pairing.dest}`}
                  className="flex items-center gap-2 rounded border border-border bg-card px-2 py-1.5"
                >
                  <span className="truncate font-mono text-xs">{pairing.source}</span>
                  <Icon icon={UiArrowRight} className="shrink-0 text-[12px] text-muted-foreground" />
                  <span className="truncate font-mono text-xs">{pairing.dest}</span>
                  <button
                    type="button"
                    aria-label={`Remove the ${pairing.source} pairing`}
                    className="ml-auto rounded p-1 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                    onClick={() =>
                      onChange({ ...state, pairings: state.pairings.filter((_, i) => i !== index) })
                    }
                  >
                    <Icon icon={UiTrash} className="text-[13px]" />
                  </button>
                </div>
              ))}

              <Button
                variant="outline"
                size="sm"
                className="w-full gap-1.5"
                disabled={!canPair}
                onClick={addPairing}
              >
                <Icon icon={UiAdd} className="text-[13px]" />
                {canPair ? `Pair ${sourceField} ↔ ${destField}` : "Pick a field on each side"}
              </Button>

              <div className="rounded border border-dashed border-border bg-muted/40 p-2">
                <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                  <Icon icon={UiCode2} className="text-[12px]" />
                  Generated CEL
                  <button
                    type="button"
                    className="ml-auto underline underline-offset-2 hover:text-foreground"
                    onClick={() => onChange({ ...state, mode: "cel", cel })}
                  >
                    take over
                  </button>
                </div>
                <code className="mt-1 block break-all font-mono text-[11px] text-muted-foreground">
                  {cel || "pair a field on each side"}
                </code>
              </div>
            </>
          ) : (
            <>
              <textarea
                className="h-24 w-full resize-none rounded border border-border bg-card p-2 font-mono text-[11px]"
                value={state.cel}
                onChange={(event) => onChange({ ...state, cel: event.target.value })}
                aria-label="Key CEL expression"
              />
              <p className="text-xs text-muted-foreground">
                A hand-written expression replaces the mapping — the engine takes columns or CEL, never both. The
                row is bound as <code className="font-mono">row</code>, and every field name that is a valid
                identifier is bound on its own.
              </p>
            </>
          )}

          <div className="space-y-1 border-t border-border/60 pt-2">
            <div className="flex items-center gap-1.5 text-[11px] font-medium">
              <Icon icon={UiClock} className="text-[12px]" />
              Event time
            </div>
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <Clock side="source" field={timestampField(source)} />
              <Clock side="dest" field={timestampField(dest)} />
            </div>
          </div>
        </section>

        <section className="rounded-lg border border-border p-3">
          <div className="flex items-center gap-2">
            <SideTag side="dest" />
            <div className="min-w-0 flex-1">
              <ProfilePicker
                names={destNames}
                value={state.dest}
                onChange={(dest) => onChange({ ...state, dest })}
                ariaLabel="Destination profile"
              />
            </div>
          </div>
          <FieldList
            fields={profileFields(dest)}
            selected={destField}
            onSelect={setDestField}
            clock={timestampField(dest)}
          />
        </section>
      </div>

      <section className="space-y-3 rounded-lg border border-border p-3">
        {routed.length > 0 && (
          <div className="space-y-1">
            <h3 className="text-sm font-semibold">Filters</h3>
            <div className="flex flex-wrap items-center gap-2">
              {routed.map((param) => (
                <span
                  key={param.name}
                  className={cn(
                    "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs",
                    param.dropped ? "border-amber-500/50 bg-amber-500/10" : "border-border bg-muted/60",
                  )}
                >
                  <span className="font-mono">
                    {param.name}={param.value}
                  </span>
                  {param.sides.map((side) => (
                    <SideTag key={side} side={side} />
                  ))}
                  {param.dropped && (
                    <span className="text-amber-700 [[data-theme=dark]_&]:text-amber-400">
                      dropped on {param.sides.includes("source") ? "the destination" : "the source"}
                    </span>
                  )}
                </span>
              ))}
            </div>
          </div>
        )}

        <div className="flex flex-wrap items-end gap-3 border-t border-border/60 pt-3">
          <label className="flex w-40 flex-col gap-1 text-xs text-muted-foreground">
            Rows per side
            <InputField
              type="number"
              min={0}
              value={state.limit === 0 ? "" : String(state.limit)}
              placeholder="all"
              onChange={(text) => onChange({ ...state, limit: Number(text) || 0 })}
            />
          </label>
          <Button size="sm" className="gap-1.5" disabled={running || !state.dest || !cel} onClick={onRun}>
            <Icon icon={UiPlay} className="text-[13px]" />
            {running ? "Reconciling…" : "Reconcile"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={saving || !state.dest || !cel}
            onClick={onSave}
            title="Store this reconcile on the source profile so it runs again without retyping"
          >
            {saving ? "Saving…" : "Save on profile"}
          </Button>
          <p className="text-xs text-muted-foreground">
            A bound makes checking a key cheap; a bounded run reports a key as one-sided when its counterpart falls
            outside the window.
          </p>
        </div>

        {error && <p className="text-xs text-destructive">{error}</p>}
      </section>
    </div>
  );
}

function SideHeader({ side, name }: { side: "source" | "dest"; name: string }) {
  return (
    <div className="flex items-center gap-2">
      <SideTag side={side} />
      <span className="truncate text-sm font-semibold">{name}</span>
    </div>
  );
}

function SideTag({ side }: { side: "source" | "dest" }) {
  return (
    <span
      className={cn(
        "rounded px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wide",
        side === "source"
          ? "bg-sky-500/10 text-sky-700 [[data-theme=dark]_&]:text-sky-300"
          : "bg-violet-500/10 text-violet-700 [[data-theme=dark]_&]:text-violet-300",
      )}
    >
      {side === "source" ? "src" : "dest"}
    </span>
  );
}

function Clock({ side, field }: { side: "source" | "dest"; field: string }) {
  return (
    <span className="flex items-center gap-1">
      <SideTag side={side} />
      {field ? (
        <code className="font-mono">{field}</code>
      ) : (
        <Badge tone="neutral" variant="soft" size="md">
          no timestamp column
        </Badge>
      )}
    </span>
  );
}

function FieldList({
  fields,
  selected,
  onSelect,
  clock,
}: {
  fields: string[];
  selected: string | undefined;
  onSelect: (field: string) => void;
  clock: string;
}) {
  if (fields.length === 0) {
    return (
      <p className="mt-2 text-xs text-muted-foreground">
        This profile declares no columns, so it offers no fields to pair. Write the key as CEL, or declare the
        columns on the profile.
      </p>
    );
  }
  return (
    <ul className="mt-2 max-h-64 space-y-0.5 overflow-auto">
      {fields.map((field) => (
        <li key={field}>
          <button
            type="button"
            onClick={() => onSelect(field)}
            className={cn(
              "flex w-full items-center gap-2 rounded px-2 py-1 text-left text-xs hover:bg-muted",
              selected === field && "bg-primary/10 text-primary ring-1 ring-primary/30",
            )}
          >
            <span className="truncate font-mono">{field}</span>
            {field === clock && (
              <Icon icon={UiClock} className="ml-auto shrink-0 text-[12px] text-muted-foreground" />
            )}
          </button>
        </li>
      ))}
    </ul>
  );
}
