import {
  Button,
  Modal,
  type ClickyNode,
  type OperationsApiClient,
  type ResolvedOperation,
} from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import { useDeferredValue, useMemo, useState } from "react";
import { ProfileWizardQueryStep } from "./profileWizardQueryStep";
import {
  profileConnectionID,
  profileWizardErrorMessage,
  profileWizardStepReady,
  profileWizardSteps,
  providerTypeFromConnectionLabel,
  type ProfileColumn,
  type ProfileWizardDraft,
} from "./profileWizardModel";
import {
  FieldsStep,
  ReviewStep,
  SourceStep,
  WizardProgress,
  stepHelp,
  type ConnectionChoice,
} from "./profileWizardSteps";

type ProfileWizardProps = {
  client: OperationsApiClient;
  action: ResolvedOperation;
  initialValue: Record<string, unknown>;
  onClose: () => void;
  onSuccess: () => void | Promise<void>;
};

export function ProfileWizard({
  client,
  action,
  initialValue,
  onClose,
  onSuccess,
}: ProfileWizardProps) {
  const initialDraft = useMemo(() => cloneInitialDraft(initialValue), [initialValue]);
  const [stepIndex, setStepIndex] = useState(0);
  const [draft, setDraft] = useState<ProfileWizardDraft>(initialDraft);
  const [discovered, setDiscovered] = useState<ProfileColumn[]>(
    initialDraft.columns ?? [],
  );
  const [activeField, setActiveField] = useState(
    initialDraft.columns?.[0]?.name ?? "",
  );
  const [connectionSearch, setConnectionSearch] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const deferredConnectionSearch = useDeferredValue(connectionSearch);
  const connections = useQuery({
    queryKey: ["profile-wizard-connections", deferredConnectionSearch],
    queryFn: () => {
      if (!client.lookupFilterOptions) {
        throw new Error("Connection lookup is unavailable");
      }
      return client.lookupFilterOptions(
        "/api/v1/connection",
        "GET",
        "connection",
        deferredConnectionSearch,
      );
    },
    retry: 0,
  });
  const connectionChoices = useMemo(
    () =>
      Object.entries(connections.data?.options ?? {})
        .map(([value, node]) => connectionChoice(value, node))
        .filter((choice): choice is ConnectionChoice => choice != null)
        .sort((left, right) => left.name.localeCompare(right.name)),
    [connections.data?.options],
  );
  const step = profileWizardSteps[stepIndex];
  const connectionID = profileConnectionID(draft.provider?.connection ?? "");
  const currentStepReady = profileWizardStepReady(
    step.id,
    draft,
    discovered,
  );

  const updateQueryDraft = (next: ProfileWizardDraft) => {
    if (next.query !== draft.query) setDiscovered([]);
    setDraft(next);
  };

  const acceptSample = (columns: ProfileColumn[]) => {
    setDiscovered(columns);
    setActiveField((current) =>
      columns.some((field) => field.name === current)
        ? current
        : (columns[0]?.name ?? ""),
    );
    setDraft((current) => {
      const configuredByName = new Map(
        (current.columns ?? []).map((field) => [field.name, field]),
      );
      return {
        ...current,
        columns:
          configuredByName.size === 0
            ? columns
            : columns
                .filter((field) => configuredByName.has(field.name))
                .map((field) => ({
                  ...field,
                  ...configuredByName.get(field.name),
                })),
      };
    });
  };

  const save = async () => {
    setSaving(true);
    setError("");
    try {
      if (!client.submitForm) {
        throw new Error("Profile creation is unavailable");
      }
      const response = await client.submitForm(action.path, action.method, draft);
      if (!response.success) {
        throw new Error(
          response.error || response.message || "Profile creation failed",
        );
      }
      await onSuccess();
    } catch (saveError) {
      setError(profileWizardErrorMessage(saveError, "Profile creation failed"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      confirmClose={
        stepIndex > 0 || draft.query
          ? {
              title: "Discard this profile?",
              message: "Your query and field configuration will be lost.",
              confirmLabel: "Discard profile",
            }
          : false
      }
      title="Build a profile"
      subtitle={
        <WizardProgress
          stepIndex={stepIndex}
          onStepChange={(next) => {
            if (next < stepIndex) setStepIndex(next);
          }}
        />
      }
      size="full"
      className="h-[calc(100dvh-2rem)]"
      scrollBody={false}
      footer={
        <div className="flex w-full items-center gap-3">
          <span className="text-xs text-muted-foreground">
            Step {stepIndex + 1} of {profileWizardSteps.length}
          </span>
          {error ? (
            <span className="ml-3 text-sm text-destructive">{error}</span>
          ) : null}
          <div className="ml-auto flex gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            {stepIndex > 0 ? (
              <Button
                type="button"
                variant="outline"
                disabled={saving}
                onClick={() => setStepIndex((current) => current - 1)}
              >
                Back
              </Button>
            ) : null}
            {stepIndex < profileWizardSteps.length - 1 ? (
              <Button
                type="button"
                disabled={!currentStepReady}
                onClick={() => setStepIndex((current) => current + 1)}
              >
                Continue
              </Button>
            ) : (
              <Button
                type="button"
                disabled={!currentStepReady || saving}
                onClick={() => void save()}
              >
                {saving ? "Saving…" : "Save profile"}
              </Button>
            )}
          </div>
        </div>
      }
    >
      <div className="flex h-full min-h-0 flex-col gap-5">
        <div className="shrink-0">
          <p className="text-xs font-semibold uppercase tracking-[0.18em] text-primary">
            {step.description}
          </p>
          <h2 className="mt-1 text-xl font-semibold">{step.label}</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            {stepHelp[step.id]}
          </p>
        </div>
        <div key={step.id} className="min-h-0 flex-1 overflow-auto">
          {step.id === "source" ? (
            <SourceStep
              choices={connectionChoices}
              loading={connections.isLoading}
              error={connections.error}
              truncated={connections.data?.truncated}
              total={connections.data?.total}
              search={connectionSearch}
              selected={draft.provider?.connection ?? ""}
              selectedType={draft.provider?.type ?? ""}
              onSearchChange={setConnectionSearch}
              onSelect={(choice) => {
                if (choice.value === draft.provider?.connection) return;
                setDraft({
                  namespace: draft.namespace,
                  provider: {
                    type: choice.providerType,
                    connection: choice.value,
                  },
                });
                setDiscovered([]);
                setActiveField("");
              }}
            />
          ) : null}
          {step.id === "query" && connectionID ? (
            <ProfileWizardQueryStep
              key={connectionID}
              connectionID={connectionID}
              draft={draft}
              discovered={discovered}
              onDraftChange={updateQueryDraft}
              onSample={acceptSample}
            />
          ) : null}
          {step.id === "fields" ? (
            <FieldsStep
              draft={draft}
              discovered={discovered}
              activeField={activeField}
              onDraftChange={setDraft}
              onActiveFieldChange={setActiveField}
            />
          ) : null}
          {step.id === "review" ? (
            <ReviewStep draft={draft} discovered={discovered} />
          ) : null}
        </div>
      </div>
    </Modal>
  );
}

function cloneInitialDraft(value: Record<string, unknown>): ProfileWizardDraft {
  const draft = value as ProfileWizardDraft;
  return {
    ...draft,
    provider: draft.provider ? { ...draft.provider } : {},
    columns: draft.columns?.map((column) => ({ ...column })) ?? [],
  };
}

function connectionChoice(
  value: string,
  node: ClickyNode,
): ConnectionChoice | null {
  const label = clickyNodeText(node) || value;
  const providerType = providerTypeFromConnectionLabel(label);
  if (!providerType) return null;
  return {
    value,
    label,
    name: profileConnectionID(value) ?? label,
    providerType,
  };
}

function clickyNodeText(node: ClickyNode): string {
  if (node.plain) return node.plain;
  if (node.text) return node.text;
  return (node.children ?? []).map(clickyNodeText).join("");
}
