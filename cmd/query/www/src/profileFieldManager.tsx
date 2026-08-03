import { Button } from "@flanksource/clicky-ui";
import { ProfileFieldEditor } from "./profileFieldEditor";
import { ProfileFieldFilters } from "./profileFieldGrid";
import { useProfileFieldState, type ProfileFieldStateProps } from "./profileFieldState";

/**
 * The wizard's two-pane column step: a field list beside the inspector.
 *
 * The editor route spreads the same state across Workspace panes instead; both
 * read `useProfileFieldState`, so selection and filtering behave identically.
 */
export function ProfileFieldManager(props: ProfileFieldStateProps) {
  const state = useProfileFieldState(props);
  const { activeField } = state;

  return (
    <div className="grid min-h-0 gap-4 lg:grid-cols-[minmax(22rem,0.9fr)_minmax(24rem,1.1fr)]">
      <section className="flex min-h-0 flex-col overflow-hidden rounded-xl border bg-card">
        <div className="space-y-3 border-b p-4">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h3 className="font-semibold">Fields</h3>
              <p className="text-xs text-muted-foreground">
                {state.configuredCount} of {state.available.length} fields selected
              </p>
            </div>
            <div className="flex items-center gap-2 text-xs">
              <Button type="button" size="sm" variant="outline" onClick={state.addField}>
                Add column
              </Button>
              <button
                type="button"
                className="font-medium text-primary hover:underline"
                onClick={() => state.setVisibleSelection(true)}
              >
                Select visible
              </button>
              <span className="text-border">|</span>
              <button
                type="button"
                className="font-medium text-muted-foreground hover:text-foreground"
                onClick={() => state.setVisibleSelection(false)}
              >
                Clear visible
              </button>
            </div>
          </div>
          <ProfileFieldFilters state={state} />
        </div>
        <div className="min-h-64 flex-1 overflow-auto" role="list">
          {state.visibleFields.map((field) => (
            <div
              key={field.name}
              role="listitem"
              className={`flex w-full items-center gap-3 border-b px-4 py-2.5 text-sm transition-colors ${
                activeField?.name === field.name ? "bg-primary/8" : "hover:bg-muted/50"
              }`}
            >
              <input
                type="checkbox"
                checked={state.selectedNames.has(field.name)}
                aria-label={`Include ${field.name}`}
                onChange={(event) => state.setFieldSelection(field, event.target.checked)}
              />
              <button
                type="button"
                className="flex min-w-0 flex-1 items-center gap-3 text-left"
                onClick={() => state.setActiveName(field.name)}
              >
                <span className="min-w-0 flex-1 truncate font-mono text-xs">
                  {field.name}
                </span>
                <span className="rounded bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">
                  {field.type || "auto"}
                </span>
              </button>
            </div>
          ))}
          {state.visibleFields.length === 0 ? (
            <p className="p-8 text-center text-sm text-muted-foreground">
              No fields match these filters.
            </p>
          ) : null}
        </div>
      </section>

      <ProfileFieldEditor
        field={activeField}
        selected={Boolean(activeField && state.selectedNames.has(activeField.name))}
        onSelectedChange={(selected) => {
          if (activeField) state.setFieldSelection(activeField, selected);
        }}
        onChange={state.updateActiveField}
        canMoveUp={state.canMoveUp}
        canMoveDown={state.canMoveDown}
        onMoveUp={() => state.moveActive(-1)}
        onMoveDown={() => state.moveActive(1)}
        onRemove={state.removeActive}
      />
    </div>
  );
}
