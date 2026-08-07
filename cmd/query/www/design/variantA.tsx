import { Button, Modal, Tabs } from "@flanksource/clicky-ui";
import { useState } from "react";
import { sections, type SectionKey } from "./mockProfile";
import {
  FieldDetailForm,
  FieldFilters,
  FieldRow,
  InspectorActions,
  useFieldState,
} from "./shared";

/**
 * Variant A — Modal, top tabs, fixed two-pane master/detail.
 *
 * The design that profileFieldManager.tsx already intends, made to actually
 * render: an explicit two-column grid where each pane owns its own scroll, so
 * the inspector is always on screen next to the list instead of stacked a full
 * viewport below it. Smallest possible diff from what ships today.
 */
export function VariantA({ onClose }: { onClose: () => void }) {
  const state = useFieldState();
  const [tab, setTab] = useState<SectionKey>("columns");

  return (
    <Modal
      open
      onClose={onClose}
      title="Edit os2"
      subtitle="Profile workspace"
      size="full"
      scrollBody={false}
      className="design-dialog-tall"
      footer={
        <div className="flex w-full items-center gap-3">
          <span className="text-xs text-muted-foreground">Unsaved changes</span>
          <div className="ml-auto flex gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button type="button">Save profile</Button>
          </div>
        </div>
      }
    >
      <div className="flex h-full min-h-0 flex-col">
        <Tabs
          tabs={sections.map((section) => ({ id: section.key, label: section.label }))}
          value={tab}
          onChange={(next) => setTab(next as SectionKey)}
          className="shrink-0"
        />

        {tab === "columns" ? (
          <div className="mt-4 grid min-h-0 flex-1 grid-cols-[26rem_minmax(0,1fr)] gap-4">
            <section className="flex min-h-0 flex-col overflow-hidden rounded-xl border border-border bg-card">
              <div className="shrink-0 space-y-2 border-b border-border p-3">
                <div className="flex items-center justify-between gap-2">
                  <div>
                    <h3 className="text-sm font-semibold">Fields</h3>
                    <p className="text-[11px] text-muted-foreground">
                      {state.includedCount} of {state.fields.length} selected
                    </p>
                  </div>
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

            <section className="flex min-h-0 flex-col overflow-hidden rounded-xl border border-border bg-card">
              {state.activeField ? (
                <>
                  <div className="flex shrink-0 items-center gap-2 border-b border-border px-4 py-2.5">
                    <div className="mr-auto min-w-0">
                      <p className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                        Field editor
                      </p>
                      <h3 className="truncate font-mono text-sm font-semibold">
                        {state.activeField.name}
                      </h3>
                    </div>
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
          <PlaceholderSection tab={tab} />
        )}
      </div>
    </Modal>
  );
}

export function PlaceholderSection({ tab }: { tab: SectionKey }) {
  const section = sections.find((item) => item.key === tab);
  return (
    <div className="mt-4 grid min-h-0 flex-1 place-items-center rounded-xl border border-dashed border-border text-sm text-muted-foreground">
      <div className="text-center">
        <p className="font-medium text-foreground">{section?.label}</p>
        <p className="mt-1 text-xs">{section?.hint}</p>
      </div>
    </div>
  );
}
