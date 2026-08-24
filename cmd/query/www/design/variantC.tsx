import { Button, Workspace, type WorkspacePaneSpec } from "@flanksource/clicky-ui";
import { UiColumns, UiListTree, UiSliders, UiTable } from "@flanksource/clicky-ui/icons";
import { useMemo, useState } from "react";
import { sections, type SectionKey } from "./mockProfile";
import {
  FieldDetailForm,
  FieldFilters,
  FieldGrid,
  InspectorActions,
  PreviewTable,
  SectionRail,
  useFieldState,
} from "./shared";

/**
 * Variant C — full-page route (/profiles/:id/edit) built on clicky-ui Workspace.
 *
 * Workspace owns the panes: each one gets a labelled header, a collapse toggle,
 * a drag handle, and layout persisted to localStorage under `storageKey`. That
 * replaces the hand-rolled rail/inspector/preview chrome C used to carry, and
 * it means the user — not us — decides how much room the field grid gets.
 *
 * The editing idea is unchanged: fields are an editable grid (include, label,
 * type, width in place), and the inspector only holds what a grid cell cannot.
 */
export function VariantC({ onClose }: { onClose: () => void }) {
  const state = useFieldState();
  const [section, setSection] = useState<SectionKey>("columns");
  const active = sections.find((item) => item.key === section);

  const panes = useMemo<WorkspacePaneSpec[]>(() => {
    const nav: WorkspacePaneSpec = {
      id: "sections",
      label: "Profile",
      icon: <UiListTree />,
      location: "left",
      width: 240,
      minWidth: 180,
      maxWidth: 340,
      content: (
        <SectionRail
          value={section}
          onChange={setSection}
          className="w-full border-r-0 bg-transparent"
        />
      ),
    };

    if (section !== "columns") {
      return [
        nav,
        {
          id: "section",
          label: active?.label ?? "Section",
          location: "center",
          collapsible: false,
          content: (
            <div className="grid h-full place-items-center text-center text-sm text-muted-foreground">
              <div>
                <p className="font-medium text-foreground">{active?.label}</p>
                <p className="mt-1 text-xs">{active?.hint}</p>
              </div>
            </div>
          ),
        },
      ];
    }

    return [
      nav,
      {
        id: "columns",
        label: "Columns",
        icon: <UiColumns />,
        location: "center",
        collapsible: false,
        contentClassName: "flex flex-col overflow-hidden",
        slots: {
          headerTrailing: (
            <>
              <span className="text-[11px] text-muted-foreground">
                {state.includedCount} of {state.fields.length} included
              </span>
              <Button type="button" size="sm" variant="outline">Add column</Button>
            </>
          ),
        },
        content: (
          <>
            <div className="shrink-0 border-b border-border p-2">
              <FieldFilters state={state} compact />
            </div>
            <FieldGrid state={state} />
          </>
        ),
      },
      {
        id: "field",
        label: state.activeField.name,
        icon: <UiSliders />,
        location: "right",
        width: 360,
        minWidth: 300,
        maxWidth: 560,
        content: (
          <div className="flex flex-col">
            <div className="flex shrink-0 items-center border-b border-border px-2 py-1.5">
              <InspectorActions
                field={state.activeField}
                onMove={(offset) => state.move(state.activeField.name, offset)}
                onToggle={(included) => state.toggle(state.activeField.name, included)}
              />
            </div>
            <div className="p-3">
              <FieldDetailForm
                field={state.activeField}
                columns={1}
                onPatch={(next) => state.patch(state.activeField.name, next)}
              />
            </div>
          </div>
        ),
      },
      {
        id: "preview",
        label: "Preview",
        icon: <UiTable />,
        location: "bottom",
        height: 200,
        minHeight: 120,
        maxHeight: 480,
        slots: {
          headerTrailing: (
            <span className="text-[11px] text-muted-foreground">
              5 sampled rows · updates as you edit
            </span>
          ),
        },
        content: <PreviewTable fields={state.fields} />,
      },
    ];
  }, [active, section, state]);

  return (
    <div className="grid h-full min-h-0 grid-rows-[auto_minmax(0,1fr)] bg-background">
      <header className="flex items-center gap-3 border-b border-border px-4 py-2.5">
        <nav className="flex items-center gap-1.5 text-sm text-muted-foreground">
          <button type="button" className="hover:text-foreground" onClick={onClose}>Profiles</button>
          <span>/</span>
          <button type="button" className="hover:text-foreground" onClick={onClose}>os2</button>
          <span>/</span>
          <span className="font-medium text-foreground">Edit</span>
        </nav>
        <span className="ml-auto rounded-full bg-warning/15 px-2 py-0.5 text-[11px] text-warning-foreground">
          Unsaved changes
        </span>
        <Button type="button" size="sm" variant="ghost" onClick={onClose}>Discard</Button>
        <Button type="button" size="sm">Save profile</Button>
      </header>

      <Workspace panes={panes} storageKey="design-variant-c" />
    </div>
  );
}
