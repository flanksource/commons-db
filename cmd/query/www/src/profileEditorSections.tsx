import { JsonSchemaForm } from "@flanksource/clicky-ui";
import type { ReactNode } from "react";
import {
  mergeProfileProjection,
  profileSchemaProjection,
  providerOptionsSchema,
  providerTypes,
} from "./profileEditorModel";
import { ProfileWizardQueryStep, type ProfileSample } from "./profileWizardQueryStep";
import {
  profileConnectionID,
  type ProfileColumn,
  type ProfileWizardDraft,
} from "./profileWizardModel";

const inputClassName =
  "w-full rounded-md border border-input bg-background px-3 py-2 text-sm outline-none focus:border-primary focus:ring-2 focus:ring-primary/15";

export function ProfileGeneralSection({
  draft,
  onChange,
}: EditorSectionProps) {
  return (
    <SectionCard
      title="Profile identity"
      description="Name the profile and choose its default presentation. Renaming updates dependent imports when you save."
    >
      <div className="grid gap-5 md:grid-cols-2">
        <EditorField label="Profile name" required>
          <input
            value={draft.profile ?? ""}
            className={inputClassName}
            onChange={(event) => onChange({ ...draft, profile: event.target.value })}
          />
        </EditorField>
        <EditorField label="Namespace">
          <input
            value={draft.namespace ?? ""}
            className={inputClassName}
            placeholder="Default namespace"
            onChange={(event) => onChange({ ...draft, namespace: event.target.value })}
          />
        </EditorField>
        <EditorField label="Render mode">
          <select
            value={typeof draft.render === "string" ? draft.render : ""}
            className={inputClassName}
            onChange={(event) => onChange({ ...draft, render: event.target.value || undefined })}
          >
            <option value="">Table (default)</option>
            <option value="table">Table</option>
            <option value="logs">Logs</option>
          </select>
        </EditorField>
      </div>
    </SectionCard>
  );
}

export function ProfileSourceSection({
  draft,
  discovered,
  sampleStale,
  onChange,
  onSample,
}: EditorSectionProps & {
  discovered: ProfileColumn[];
  sampleStale: boolean;
  onSample: (sample: ProfileSample) => void;
}) {
  const connectionID = profileConnectionID(draft.provider?.connection ?? "");
  const providerType = draft.provider?.type ?? "";
  return (
    <div className="grid h-full min-h-0 gap-4">
      <SectionCard
        title="Source"
        description="Select the provider and saved connection. Connection credentials remain server-side."
      >
        <div className="grid gap-4 md:grid-cols-2">
          <EditorField label="Provider type" required>
            <select
              value={providerType}
              className={inputClassName}
              onChange={(event) =>
                onChange({
                  ...draft,
                  provider: { ...(draft.provider ?? {}), type: event.target.value },
                })
              }
            >
              <option value="">Choose a provider</option>
              {providerTypes().map((type) => <option key={type} value={type}>{type}</option>)}
            </select>
          </EditorField>
          <EditorField label="Saved connection or inline URL">
            <input
              value={draft.provider?.connection ?? ""}
              className={inputClassName}
              placeholder="connection://name"
              onChange={(event) =>
                onChange({
                  ...draft,
                  provider: { ...(draft.provider ?? {}), connection: event.target.value },
                })
              }
            />
          </EditorField>
        </div>
        {sampleStale ? (
          <p className="mt-4 rounded-lg border border-warning/40 bg-warning/10 px-4 py-3 text-sm text-warning-foreground">
            Source or query settings changed after the latest sample. You can save,
            but sampling again will verify the current field shape.
          </p>
        ) : null}
      </SectionCard>
      {connectionID ? (
        <ProfileWizardQueryStep
          key={connectionID}
          connectionID={connectionID}
          draft={draft}
          discovered={discovered}
          onDraftChange={onChange}
          onSample={onSample}
        />
      ) : (
        <SectionCard
          title="Query and provider options"
          description="Inline sources use the provider schema for options while keeping the query workspace explicit."
        >
          <EditorField label="Query">
            <textarea
              rows={10}
              value={draft.query ?? ""}
              className={`${inputClassName} resize-y font-mono text-xs`}
              onChange={(event) => onChange({ ...draft, query: event.target.value })}
            />
          </EditorField>
          <div className="mt-5">
            <JsonSchemaForm
              idPrefix="profile-provider-options"
              schema={providerOptionsSchema(providerType)}
              value={draft.provider?.options ?? {}}
              onChange={(options) =>
                onChange({
                  ...draft,
                  provider: { ...(draft.provider ?? {}), options },
                })
              }
              showPreferencesMenu={false}
            />
          </div>
        </SectionCard>
      )}
    </div>
  );
}

export function ProfileSchemaSection({
  draft,
  keys,
  title,
  description,
  idPrefix,
  onChange,
}: EditorSectionProps & {
  keys: string[];
  title: string;
  description: string;
  idPrefix: string;
}) {
  return (
    <SectionCard title={title} description={description}>
      <JsonSchemaForm
        idPrefix={idPrefix}
        schema={profileSchemaProjection(keys)}
        value={Object.fromEntries(keys.flatMap((key) => Object.prototype.hasOwnProperty.call(draft, key) ? [[key, draft[key]]] : []))}
        onChange={(next) => onChange(mergeProfileProjection(draft, keys, next))}
        showPreferencesMenu={false}
      />
    </SectionCard>
  );
}

type EditorSectionProps = {
  draft: ProfileWizardDraft;
  onChange: (draft: ProfileWizardDraft) => void;
};

function SectionCard({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: ReactNode;
}) {
  return (
    <section className="rounded-xl border bg-card p-5">
      <h2 className="text-lg font-semibold">{title}</h2>
      <p className="mb-5 mt-1 text-sm text-muted-foreground">{description}</p>
      {children}
    </section>
  );
}

function EditorField({
  label,
  required,
  children,
}: {
  label: string;
  required?: boolean;
  children: ReactNode;
}) {
  return (
    <label className="grid gap-1.5 text-sm font-medium">
      <span>{label}{required ? <span className="text-destructive"> *</span> : null}</span>
      {children}
    </label>
  );
}
