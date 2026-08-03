import { Button, Modal } from "@flanksource/clicky-ui";
import { useState } from "react";
import { sections, type SectionKey } from "./mockProfile";
import {
  FieldDetailForm,
  FieldFilters,
  FieldRow,
  InspectorActions,
  PreviewTable,
  SectionRail,
  useFieldState,
} from "./shared";

/**
 * Variant D — modal as a three-pane workspace with a live preview dock.
 *
 * Keeps the overlay (so the route never changes and Escape still cancels) but
 * gives up top tabs for a rail, and adds a collapsible bottom dock rendering
 * the current column config against sampled rows. You see what you are
 * building without leaving the editor — the thing every other variant makes
 * you close the dialog to check.
 */
export function VariantD({ onClose }: { onClose: () => void }) {
  const state = useFieldState();
  const [section, setSection] = useState<SectionKey>("columns");
  const [dock, setDock] = useState(true);

  return (
    <Modal
      open
      onClose={onClose}
      title="Edit os2"
      size="full"
      scrollBody={false}
      className="design-dialog-tall design-dialog-flush"
      headerSlot={
        <span className="mr-2 rounded-full bg-warning/15 px-2 py-0.5 text-[11px] text-warning-foreground">
          Unsaved changes
        </span>
      }
      footer={
        <div className="flex w-full items-center gap-3">
          <button
            type="button"
            className="rounded px-2 py-1 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
            onClick={() => setDock((current) => !current)}
          >
            {dock ? "Hide preview" : "Show preview"}
          </button>
          <span className="text-xs text-muted-foreground">
            {state.includedCount} of {state.fields.length} columns included
          </span>
          <div className="ml-auto flex gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button type="button">Save profile</Button>
          </div>
        </div>
      }
    >
      <div className="flex h-full min-h-0 flex-col">
        <div className="flex min-h-0 flex-1">
          <SectionRail value={section} onChange={setSection} className="w-52" />

          {section === "columns" ? (
            <>
              <section className="flex min-h-0 w-[23rem] shrink-0 flex-col overflow-hidden border-r border-border">
                <div className="shrink-0 space-y-2 border-b border-border p-2.5">
                  <div className="flex items-center justify-between gap-2">
                    <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                      Fields
                    </h3>
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

              <section className="flex min-h-0 flex-1 flex-col overflow-hidden">
                {state.activeField ? (
                  <>
                    <div className="flex shrink-0 items-center gap-2 border-b border-border px-4 py-2">
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
            </>
          ) : (
            <div className="grid min-h-0 flex-1 place-items-center text-sm text-muted-foreground">
              <div className="text-center">
                <p className="font-medium text-foreground">
                  {sections.find((item) => item.key === section)?.label}
                </p>
                <p className="mt-1 text-xs">{sections.find((item) => item.key === section)?.hint}</p>
              </div>
            </div>
          )}
        </div>

        {dock ? (
          <section className="flex h-48 shrink-0 flex-col border-t border-border">
            <div className="flex shrink-0 items-center gap-2 bg-muted/40 px-3 py-1.5">
              <h3 className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
                Live preview
              </h3>
              <span className="text-[11px] text-muted-foreground">
                5 sampled rows · updates as you edit
              </span>
            </div>
            <PreviewTable fields={state.fields} dense />
          </section>
        ) : null}
      </div>
    </Modal>
  );
}
