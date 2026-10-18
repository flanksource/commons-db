import { mountPath } from "./mount";
import {
  Button,
  Modal,
  OperationsApiClientError,
  SchemaActionForm,
  Select,
  useOperations,
  type ExecutionResponse,
  type FormActionsRenderer,
  type PostExtension,
  type PreExtension,
  type ResolvedOperation,
  type SharedOperationsApiClient,
} from "@flanksource/clicky-ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo, useState, type ReactNode } from "react";

export type ProfileConnectionRequired = {
  code: "profile_connection_required";
  profile: string;
  mappingProfile: string;
  connectionType: "opentelemetry";
  mappingUrl: string;
};

type PendingMapping = {
  required: ProfileConnectionRequired;
  retry: () => Promise<ExecutionResponse>;
  resolve: (response: ExecutionResponse) => void;
  reject: (error: unknown) => void;
  error: unknown;
};

export function isProfileConnectionRequired(value: unknown): value is ProfileConnectionRequired {
  if (value == null || typeof value !== "object") return false;
  const payload = value as Record<string, unknown>;
  return (
    payload.code === "profile_connection_required" &&
    typeof payload.profile === "string" &&
    typeof payload.mappingProfile === "string" &&
    payload.connectionType === "opentelemetry" &&
    typeof payload.mappingUrl === "string"
  );
}

export function findConnectionCreateOperation(
  operations: ResolvedOperation[],
): ResolvedOperation | undefined {
  return operations.find((operation) => {
    const metadata = operation.operation["x-clicky"];
    return (
      metadata?.surface === "connection" &&
      metadata.scope === "collection" &&
      metadata.verb === "create"
    );
  });
}

export function profileConnectionOptions(filter: {
  options?: Record<string, unknown>;
}): Array<{ value: string; label: string }> {
  return Object.keys(filter.options ?? {}).map((value) => ({
    value,
    label: value.replace(/^connection:\/\//, ""),
  }));
}

export function rejectPendingMapping(current: Pick<PendingMapping, "reject" | "error"> | null): void {
  current?.reject(current.error);
}

export function useProfileConnectionMapping(base: SharedOperationsApiClient): {
  client: SharedOperationsApiClient;
  dialog: ReactNode;
} {
  const [pending, setPending] = useState<PendingMapping | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const waitForMapping = useCallback(
    (required: ProfileConnectionRequired, retry: () => Promise<ExecutionResponse>, error: unknown) =>
      new Promise<ExecutionResponse>((resolve, reject) => {
        setPending((current) => {
          rejectPendingMapping(current);
          return { required, retry, resolve, reject, error };
        });
      }),
    [],
  );
  const client = useMemo<SharedOperationsApiClient>(
    () => ({
      ...base,
      async executeCommand(path, method, params, headers) {
        try {
          return await base.executeCommand(path, method, params, headers);
        } catch (error) {
          if (
            error instanceof OperationsApiClientError &&
            error.status === 409 &&
            isProfileConnectionRequired(error.responseData)
          ) {
            return waitForMapping(
              error.responseData,
              () => base.executeCommand(path, method, params, headers),
              error,
            );
          }
          throw error;
        }
      },
    }),
    [base, waitForMapping],
  );
  return {
    client,
    dialog: (
      <ProfileConnectionMappingDialogs
        client={client}
        pending={pending}
        createOpen={createOpen}
        onClose={() => {
          pending?.reject(pending.error);
          setPending(null);
        }}
        onMapped={(response) => {
          pending?.resolve(response);
          setPending(null);
        }}
        onCreate={() => {
          pending?.reject(pending.error);
          setPending(null);
          setCreateOpen(true);
        }}
        onCreateClose={() => setCreateOpen(false)}
      />
    ),
  };
}

function ProfileConnectionMappingDialogs({
  client,
  pending,
  createOpen,
  onClose,
  onMapped,
  onCreate,
  onCreateClose,
}: {
  client: SharedOperationsApiClient;
  pending: PendingMapping | null;
  createOpen: boolean;
  onClose: () => void;
  onMapped: (response: ExecutionResponse) => void;
  onCreate: () => void;
  onCreateClose: () => void;
}) {
  const { operations } = useOperations(client);
  const createAction = findConnectionCreateOperation(operations);
  return (
    <>
      <ConnectTraceProfileModal
        client={client}
        pending={pending}
        createAction={createAction}
        onClose={onClose}
        onMapped={onMapped}
        onCreate={onCreate}
      />
      {createOpen && createAction ? (
        <AddConnectionForm
          client={client}
          action={createAction}
          onClose={onCreateClose}
        />
      ) : null}
    </>
  );
}

function ConnectTraceProfileModal({
  client,
  pending,
  createAction,
  onClose,
  onMapped,
  onCreate,
}: {
  client: SharedOperationsApiClient;
  pending: PendingMapping | null;
  createAction: ResolvedOperation | undefined;
  onClose: () => void;
  onMapped: (response: ExecutionResponse) => void;
  onCreate: () => void;
}) {
  const [selected, setSelected] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const connections = useQuery({
    queryKey: ["profile-opentelemetry-connections"],
    enabled: pending != null,
    queryFn: () =>
      client.lookupFilterOptions(
        `${mountPath}/api/v1/connection`,
        "GET",
        "connection",
        "",
        { types: "opentelemetry" },
      ),
  });
  const options = profileConnectionOptions(connections.data ?? {});

  const mapAndRetry = async () => {
    if (!pending || !selected) return;
    setSaving(true);
    setError("");
    try {
      await client.submitForm(pending.required.mappingUrl, "PUT", {
        connection: selected,
      });
      onMapped(await pending.retry());
    } catch (mappingError) {
      setError(mappingError instanceof Error ? mappingError.message : String(mappingError));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open={pending != null}
      onClose={onClose}
      title="Connect trace profile"
      subtitle={
        pending
          ? `${pending.required.profile} inherits its trace backend from ${pending.required.mappingProfile}.`
          : undefined
      }
      footer={
        <div className="flex items-center justify-end gap-2">
          {error ? <span className="mr-auto text-sm text-red-600">{error}</span> : null}
          <Button variant="ghost" disabled={saving} onClick={onClose}>
            Cancel
          </Button>
          {connections.isError ? (
            <Button onClick={() => void connections.refetch()}>Retry</Button>
          ) : options.length === 0 && !connections.isLoading ? (
            <Button disabled={!createAction} onClick={onCreate}>
              Add OpenTelemetry Connection
            </Button>
          ) : (
            <Button disabled={!selected || saving} onClick={() => void mapAndRetry()}>
              {saving ? "Connecting…" : "Connect and run"}
            </Button>
          )}
        </div>
      }
    >
      <ConnectionChoice
        isLoading={connections.isLoading}
        error={connections.isError ? connections.error : undefined}
        isError={connections.isError}
        options={options}
        selected={selected}
        onSelect={setSelected}
      />
    </Modal>
  );
}

function ConnectionChoice({
  isLoading,
  isError,
  error,
  options,
  selected,
  onSelect,
}: {
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  options: Array<{ value: string; label: string }>;
  selected: string;
  onSelect: (value: string) => void;
}) {
  if (isLoading) {
    return <div className="text-sm text-muted-foreground">Loading OpenTelemetry connections…</div>;
  }
  if (isError) {
    return (
      <div className="text-sm text-red-600">
        Unable to load OpenTelemetry connections:{" "}
        {error instanceof Error ? error.message : String(error)}
      </div>
    );
  }
  if (options.length === 0) {
    return (
      <div className="text-sm text-muted-foreground">
        No OpenTelemetry connection exists yet. Add one backed by an OpenSearch connection,
        then run the profile again.
      </div>
    );
  }
  return (
    <Select
      value={selected}
      onChange={(event) => onSelect(event.target.value)}
      options={options}
      placeholder="Select an OpenTelemetry connection"
    />
  );
}

function AddConnectionForm({
  client,
  action,
  onClose,
}: {
  client: SharedOperationsApiClient;
  action: ResolvedOperation;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  return (
    <SchemaActionForm
      client={client}
      action={action}
      initialValue={{ type: "opentelemetry" }}
      submitLabel="Save Connection"
      formPre={connectionFormPre}
      formPost={connectionFormPost}
      footerActions={connectionFooterActions}
      onSuccess={async () => {
        onClose();
        await queryClient.invalidateQueries({ queryKey: ["operation-list"] });
      }}
      renderLayout={({ body, footer }) => (
        <Modal
          open
          onClose={onClose}
          title="Add OpenTelemetry Connection"
          size="lg"
          {...(footer ? { footer } : {})}
        >
          {body}
        </Modal>
      )}
      fallback={<div className="text-sm text-muted-foreground">Connection form unavailable.</div>}
    />
  );
}

let connectionFormPre: PreExtension[] | undefined;
let connectionFormPost: PostExtension[] | undefined;
let connectionFooterActions: FormActionsRenderer | undefined;

export function configureProfileConnectionForm(options: {
  formPre?: PreExtension[];
  formPost?: PostExtension[];
  footerActions?: FormActionsRenderer;
}) {
  connectionFormPre = options.formPre;
  connectionFormPost = options.formPost;
  connectionFooterActions = options.footerActions;
}
