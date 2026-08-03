import { MonacoSchemaEditor } from "@flanksource/clicky-ui/monaco";
import { useState } from "react";
import { parse, stringify } from "yaml";
import { profileEditorSchema } from "./profileEditorModel";
import type { ProfileWizardDraft } from "./profileWizardModel";

export function ProfileEditorRaw({
  draft,
  onChange,
  onValidityChange,
}: {
  draft: ProfileWizardDraft;
  onChange: (draft: ProfileWizardDraft) => void;
  onValidityChange: (valid: boolean) => void;
}) {
  const [value, setValue] = useState(() => stringify(draft));
  const [parseError, setParseError] = useState("");

  return (
    <div className="flex h-full min-h-[32rem] flex-col gap-3">
      <div className="rounded-lg border bg-muted/25 px-4 py-3 text-sm text-muted-foreground">
        Raw YAML edits the complete profile. Invalid text is kept here while you
        work; returning to another tab restores the last valid structured value.
        YAML comments and key ordering are not preserved after structured edits.
      </div>
      {parseError ? (
        <p className="text-sm text-destructive">{parseError}</p>
      ) : null}
      <div className="min-h-0 flex-1 overflow-hidden rounded-lg border">
        <MonacoSchemaEditor
          language="yaml"
          path="profile-editor.yaml"
          schemaUri="https://flanksource.com/schemas/profile.json"
          schema={profileEditorSchema}
          value={value}
          height="100%"
          onValidationChange={(state) => {
            if (!parseError) onValidityChange(state.status !== "invalid");
          }}
          onChange={(next) => {
            setValue(next);
            try {
              const parsed = parse(next);
              if (!isRecord(parsed)) {
                throw new Error("Profile YAML must contain an object");
              }
              setParseError("");
              onValidityChange(true);
              onChange(parsed as ProfileWizardDraft);
            } catch (error) {
              const message = error instanceof Error ? error.message : "Invalid YAML";
              setParseError(message);
              onValidityChange(false);
            }
          }}
        />
      </div>
    </div>
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
