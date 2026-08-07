import { Button } from "@flanksource/clicky-ui";
import { UiDownload, UiUpload } from "@flanksource/clicky-ui/icons";
import { MonacoSchemaEditor } from "@flanksource/clicky-ui/monaco";
import { useRef, useState, type ChangeEvent } from "react";
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
  const fileInput = useRef<HTMLInputElement>(null);

  const updateValue = (next: string) => {
    setValue(next);
    try {
      const parsed = parseProfileYamlDocument(next);
      setParseError("");
      onValidityChange(true);
      onChange(parsed);
    } catch (error) {
      setParseError(error instanceof Error ? error.message : "Invalid YAML");
      onValidityChange(false);
    }
  };

  const importYaml = async (event: ChangeEvent<HTMLInputElement>) => {
    const input = event.currentTarget;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    try {
      updateValue(await file.text());
    } catch (error) {
      setParseError(
        `Unable to read ${file.name}: ${error instanceof Error ? error.message : String(error)}`,
      );
      onValidityChange(false);
    }
  };

  return (
    <div className="flex h-full min-h-[32rem] flex-col gap-3">
      <div className="flex shrink-0 flex-wrap items-start gap-3 rounded-lg border bg-muted/25 px-4 py-3">
        <p className="min-w-64 flex-1 text-sm text-muted-foreground">
          Raw YAML edits the complete profile. Invalid text is kept here while you
          work; returning to another tab restores the last valid structured value.
          YAML comments and key ordering are not preserved after structured edits.
        </p>
        <div className="flex shrink-0 items-center gap-2">
          <input
            ref={fileInput}
            type="file"
            accept=".yaml,.yml,application/yaml,text/yaml,text/x-yaml"
            className="hidden"
            aria-label="Import YAML file"
            onChange={(event) => void importYaml(event)}
          />
          <Button type="button" size="sm" variant="outline" onClick={() => fileInput.current?.click()}>
            <UiUpload className="size-4" />
            Import YAML
          </Button>
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => downloadProfileYaml(value, profileYamlFilename(draft.profile))}
          >
            <UiDownload className="size-4" />
            Export YAML
          </Button>
        </div>
      </div>
      {parseError ? (
        <p className="text-sm text-destructive">{parseError}</p>
      ) : null}
      <div
        data-slot="profile-yaml-editor-frame"
        className="min-h-0 flex-1 overflow-hidden [&>[data-slot=monaco-editor]]:h-full"
      >
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
          onChange={updateValue}
        />
      </div>
    </div>
  );
}

export function parseProfileYamlDocument(value: string): ProfileWizardDraft {
  const parsed = parse(value);
  if (!isRecord(parsed)) throw new Error("Profile YAML must contain an object");
  return parsed as ProfileWizardDraft;
}

export function profileYamlFilename(name?: string): string {
  const safeName = name?.trim().replace(/[^a-zA-Z0-9._-]+/g, "-").replace(/^-+|-+$/g, "");
  return `${safeName || "profile"}.yaml`;
}

function downloadProfileYaml(value: string, filename: string) {
  const url = URL.createObjectURL(new Blob([value], { type: "application/yaml;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
