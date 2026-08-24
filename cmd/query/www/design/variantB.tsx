import { Button } from "@flanksource/clicky-ui";
import { useState } from "react";
import { sections, type SectionKey } from "./mockProfile";
import {
  FieldDetailForm,
  FieldFilters,
  FieldRow,
  InspectorActions,
  SectionRail,
  useFieldState,
} from "./shared";
import { BackdropTable } from "./variantShell";

/**
 * Variant B — right-side drawer, vertical section rail.
 *
 * Not a modal. The drawer covers ~72% of the viewport and leaves the profile
 * table visible on the left, so you keep the data you are configuring in view
 * while you edit. Top tabs become a vertical rail: section names get room for
 * a one-line hint and a state chip ("stale", "14/18"), which the horizontal
 * tab strip has nowhere to put.
 */
export function VariantB({ onClose }: { onClose: () => void }) {
  const state = useFieldState();
  const [section, setSection] = useState<SectionKey>("columns");

  return (
    <div className="fixed inset-x-0 bottom-0 top-10 z-50 flex">
      <BackdropTable onClose={onClose} />

      <aside
        role="dialog"
        aria-label="Edit profile os2"
        className="flex h-full w-[72%] min-w-[56rem] flex-col border-l border-border bg-background shadow-2xl"
      >
        <header className="flex shrink-0 items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="min-w-0">
            <h2 className="truncate text-sm font-semibold">Edit os2</h2>
            <p className="text-[11px] text-muted-foreground">
              opensearch · connection://otel-traces
            </p>
          </div>
          <span className="ml-auto rounded-full bg-warning/15 px-2 py-0.5 text-[11px] text-warning-foreground">
            Unsaved changes
          </span>
          <Button type="button" size="sm" variant="ghost" onClick={onClose}>Cancel</Button>
          <Button type="button" size="sm">Save profile</Button>
        </header>

        <div className="flex min-h-0 flex-1">
          <SectionRail value={section} onChange={setSection} />

          {section === "columns" ? (
            <div className="grid min-h-0 flex-1 grid-cols-[24rem_minmax(0,1fr)]">
              <section className="flex min-h-0 flex-col overflow-hidden border-r border-border">
                <div className="shrink-0 space-y-2 border-b border-border p-3">
                  <div className="flex items-center justify-between gap-2">
                    <p className="text-[11px] text-muted-foreground">
                      {state.includedCount} of {state.fields.length} selected
                    </p>
                    <Button type="button" size="sm" variant="outline">Add column</Button>
                  </div>
                  <FieldFilters state={state} compact />
                </div>
                <div className="min-h-0 flex-1 overflow-auto" role="list">
                  {state.visible.map((field) => (
                    <FieldRow
                      key={field.name}
                      field={field}
                      active={state.activeField?.name === field.name}
                      onSelect={() => state.setActive(field.name)}
                      onToggle={(included) => state.toggle(field.name, included)}
                    />
                  ))}
                </div>
              </section>

              <section className="flex min-h-0 flex-col overflow-hidden">
                {state.activeField ? (
                  <>
                    <div className="flex shrink-0 items-center gap-2 border-b border-border px-4 py-2.5">
                      <h3 className="mr-auto min-w-0 truncate font-mono text-sm font-semibold">
                        {state.activeField.name}
                      </h3>
                      <InspectorActions
                        field={state.activeField}
                        onMove={(offset) => state.move(state.activeField.name, offset)}
                        onToggle={(included) => state.toggle(state.activeField.name, included)}
                      />
                    </div>
                    <div className="min-h-0 flex-1 overflow-auto p-4">
                      <FieldDetailForm
                        field={state.activeField}
                        onPatch={(next) => state.patch(state.activeField.name, next)}
                      />
                    </div>
                  </>
                ) : null}
              </section>
            </div>
          ) : (
            <div className="grid min-h-0 flex-1 place-items-center text-sm text-muted-foreground">
              <div className="text-center">
                <p className="font-medium text-foreground">
                  {sections.find((item) => item.key === section)?.label}
                </p>
                <p className="mt-1 text-xs">
                  {sections.find((item) => item.key === section)?.hint}
                </p>
              </div>
            </div>
          )}
        </div>
      </aside>
    </div>
  );
}
