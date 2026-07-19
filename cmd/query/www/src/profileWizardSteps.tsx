import type { ReactNode } from "react";
import { ProfileFieldManager } from "./profileFieldManager";
import {
  profileConnectionID,
  profileWizardErrorMessage,
  profileWizardSteps,
  type ProfileColumn,
  type ProfileWizardDraft,
} from "./profileWizardModel";

export type ConnectionChoice = {
  value: string;
  label: string;
  name: string;
  providerType: string;
};

export const stepHelp = {
  source: "Start with a saved connection. We will tailor the query workspace to its provider.",
  query: "Browse the catalog, write a query, and run a safe sample to discover fields.",
  fields: "Name the profile, choose the fields to expose, and tune how each field is displayed.",
  review: "Check the source, query, and field shape before creating the profile.",
};

export function WizardProgress({
  stepIndex,
  onStepChange,
}: {
  stepIndex: number;
  onStepChange: (index: number) => void;
}) {
  return (
    <ol className="grid grid-cols-4 gap-2" aria-label="Profile builder progress">
      {profileWizardSteps.map((step, index) => (
        <li key={step.id}>
          <button
            type="button"
            disabled={index > stepIndex}
            aria-current={index === stepIndex ? "step" : undefined}
            className={`flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left ${index === stepIndex ? "bg-primary/10 text-foreground" : "text-muted-foreground"}`}
            onClick={() => onStepChange(index)}
          >
            <span
              className={`grid size-6 shrink-0 place-items-center rounded-full text-xs font-semibold ${index <= stepIndex ? "bg-primary text-primary-foreground" : "bg-muted"}`}
            >
              {index + 1}
            </span>
            <span className="hidden min-w-0 md:block">
              <span className="block truncate text-xs font-medium">{step.label}</span>
              <span className="block truncate text-[10px]">{step.description}</span>
            </span>
          </button>
        </li>
      ))}
    </ol>
  );
}

export function SourceStep({
  choices,
  loading,
  error,
  truncated,
  total,
  search,
  selected,
  selectedType,
  onSearchChange,
  onSelect,
}: {
  choices: ConnectionChoice[];
  loading: boolean;
  error: unknown;
  truncated?: boolean;
  total?: number;
  search: string;
  selected: string;
  selectedType: string;
  onSearchChange: (search: string) => void;
  onSelect: (choice: ConnectionChoice) => void;
}) {
  return (
    <div className="mx-auto grid max-w-4xl gap-5 lg:grid-cols-[1fr_18rem]">
      <section className="overflow-hidden rounded-xl border bg-card">
        <div className="border-b p-4">
          <label className="grid gap-1.5 text-sm font-medium">
            Find a saved connection
            <input
              type="search"
              value={search}
              autoFocus
              placeholder="Search connections"
              className="rounded-md border border-input bg-background px-3 py-2.5 outline-none focus:border-primary focus:ring-2 focus:ring-primary/15"
              onChange={(event) => onSearchChange(event.target.value)}
            />
          </label>
        </div>
        <div className="max-h-[26rem] overflow-auto p-2">
          {choices.map((choice) => (
            <button
              key={choice.value}
              type="button"
              className={`flex w-full items-center justify-between rounded-lg border px-4 py-3 text-left transition-colors ${selected === choice.value ? "border-primary bg-primary/8" : "border-transparent hover:bg-muted/60"}`}
              onClick={() => onSelect(choice)}
            >
              <span>
                <span className="block text-sm font-medium">{choice.name}</span>
                <span className="block text-xs text-muted-foreground">
                  Saved connection
                </span>
              </span>
              <span className="rounded-full bg-muted px-2.5 py-1 text-xs font-medium">
                {choice.providerType}
              </span>
            </button>
          ))}
          {loading ? (
            <p className="p-8 text-center text-sm text-muted-foreground">
              Loading connections…
            </p>
          ) : null}
          {error ? (
            <p className="p-8 text-center text-sm text-destructive">
              {profileWizardErrorMessage(error, "Unable to load connections")}
            </p>
          ) : null}
          {!loading && !error && choices.length === 0 ? (
            <p className="p-8 text-center text-sm text-muted-foreground">
              No saved connections match this search.
            </p>
          ) : null}
        </div>
        {truncated ? (
          <p className="border-t px-4 py-2 text-xs text-muted-foreground">
            Showing the first {choices.length} of {total ?? "many"}. Search to narrow the list.
          </p>
        ) : null}
      </section>
      <aside className="rounded-xl border bg-muted/25 p-5">
        <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Selected source
        </p>
        {selected ? (
          <>
            <h3 className="mt-3 font-semibold">{profileConnectionID(selected)}</h3>
            <p className="mt-1 text-sm text-muted-foreground">{selectedType}</p>
            <p className="mt-5 text-sm leading-6 text-muted-foreground">
              The next step loads this connection&apos;s catalog, query language,
              and sampling controls.
            </p>
          </>
        ) : (
          <p className="mt-3 text-sm leading-6 text-muted-foreground">
            Choose a connection to begin. Connection credentials stay on the
            server and are never copied into the profile.
          </p>
        )}
      </aside>
    </div>
  );
}

export function FieldsStep({
  draft,
  discovered,
  activeField,
  onDraftChange,
  onActiveFieldChange,
}: {
  draft: ProfileWizardDraft;
  discovered: ProfileColumn[];
  activeField: string;
  onDraftChange: (draft: ProfileWizardDraft) => void;
  onActiveFieldChange: (name: string) => void;
}) {
  return (
    <div className="grid gap-5">
      <section className="grid gap-4 rounded-xl border bg-muted/25 p-4 md:grid-cols-2">
        <label className="grid gap-1.5 text-sm font-medium">
          Profile name <span className="text-destructive">*</span>
          <input
            value={draft.profile ?? ""}
            placeholder="e.g. service-logs"
            className="rounded-md border border-input bg-background px-3 py-2 outline-none focus:border-primary focus:ring-2 focus:ring-primary/15"
            onChange={(event) =>
              onDraftChange({ ...draft, profile: event.target.value })
            }
          />
        </label>
        <label className="grid gap-1.5 text-sm font-medium">
          Namespace <span className="font-normal text-muted-foreground">Optional</span>
          <input
            value={draft.namespace ?? ""}
            placeholder="e.g. observability"
            className="rounded-md border border-input bg-background px-3 py-2 outline-none focus:border-primary focus:ring-2 focus:ring-primary/15"
            onChange={(event) =>
              onDraftChange({ ...draft, namespace: event.target.value })
            }
          />
        </label>
      </section>
      <ProfileFieldManager
        discovered={discovered}
        configured={draft.columns ?? []}
        activeName={activeField}
        onConfiguredChange={(columns) => onDraftChange({ ...draft, columns })}
        onActiveNameChange={onActiveFieldChange}
      />
    </div>
  );
}

export function ReviewStep({
  draft,
  discovered,
}: {
  draft: ProfileWizardDraft;
  discovered: ProfileColumn[];
}) {
  const connection = profileConnectionID(draft.provider?.connection ?? "");
  return (
    <div className="mx-auto grid max-w-5xl gap-4 lg:grid-cols-2">
      <ReviewCard label="Profile" value={draft.profile || "Unnamed"}>
        {draft.namespace ? `Namespace: ${draft.namespace}` : "Default namespace"}
      </ReviewCard>
      <ReviewCard label="Source" value={connection || "Not selected"}>
        {draft.provider?.type || "Unknown provider"}
      </ReviewCard>
      <section className="rounded-xl border bg-card p-5 lg:col-span-2">
        <div className="flex items-center justify-between gap-4">
          <div>
            <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Fields
            </p>
            <h3 className="mt-1 font-semibold">
              {draft.columns?.length ?? 0} of {discovered.length} included
            </h3>
          </div>
          <span className="rounded-full bg-primary/10 px-3 py-1 text-xs font-medium text-primary">
            {draft.columns?.filter((field) => field.hidden).length ?? 0} hidden
          </span>
        </div>
        <div className="mt-4 flex flex-wrap gap-2">
          {(draft.columns ?? []).slice(0, 16).map((field) => (
            <span
              key={field.name}
              className="rounded-md border bg-muted/30 px-2.5 py-1.5 font-mono text-xs"
            >
              {field.label || field.name}
              <span className="ml-2 text-muted-foreground">
                {field.type || "auto"}
              </span>
            </span>
          ))}
          {(draft.columns?.length ?? 0) > 16 ? (
            <span className="px-2 py-1.5 text-xs text-muted-foreground">
              +{(draft.columns?.length ?? 0) - 16} more
            </span>
          ) : null}
        </div>
      </section>
      <section className="rounded-xl border bg-card p-5 lg:col-span-2">
        <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Query
        </p>
        <pre className="mt-3 max-h-72 overflow-auto whitespace-pre-wrap rounded-lg bg-muted/50 p-4 font-mono text-xs leading-6">
          {draft.query}
        </pre>
      </section>
    </div>
  );
}

function ReviewCard({
  label,
  value,
  children,
}: {
  label: string;
  value: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="rounded-xl border bg-card p-5">
      <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {label}
      </p>
      <h3 className="mt-2 text-lg font-semibold">{value}</h3>
      <p className="mt-1 text-sm text-muted-foreground">{children}</p>
    </section>
  );
}
