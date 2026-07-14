import {
  Button,
  Icon,
  Modal,
  SchemaActionForm,
  useOperations,
  type OperationsApiClient,
  type ResolvedOperation,
} from "@flanksource/clicky-ui";
import { UiMagicWand } from "@flanksource/clicky-ui/icons";
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import {
  ProfileBuilderAutoOpen,
  profileBuilderFormExtensions,
} from "./profileBuilder";

type BuildProfileButtonProps = {
  client: OperationsApiClient;
  connectionName?: string;
  providerType?: string;
  providerOptions?: Record<string, unknown>;
};

export function buildProfileInitialValue(
  connectionName?: string,
  providerType?: string,
  providerOptions?: Record<string, unknown>,
): Record<string, unknown> {
  if (!connectionName || !providerType) return {};
  return {
    provider: {
      type: providerType,
      connection: `connection://${connectionName}`,
      ...(providerOptions && Object.keys(providerOptions).length > 0
        ? { options: providerOptions }
        : {}),
    },
  };
}

export function findProfileCreateOperation(
  operations: ResolvedOperation[],
): ResolvedOperation | undefined {
  return operations.find((operation) => {
    const meta = operation.operation["x-clicky"];
    return (
      meta?.surface === "profiles" &&
      meta.scope === "collection" &&
      meta.verb === "create"
    );
  });
}

export function BuildProfileButton({
  client,
  connectionName,
  providerType,
  providerOptions,
}: BuildProfileButtonProps) {
  const queryClient = useQueryClient();
  const { operations, isLoading } = useOperations(client);
  const createAction = findProfileCreateOperation(operations);
  const [open, setOpen] = useState(false);
  const initialValue = useMemo<Record<string, unknown>>(
    () => buildProfileInitialValue(connectionName, providerType, providerOptions),
    [connectionName, providerOptions, providerType],
  );

  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={isLoading || !createAction}
        title={
          createAction
            ? "Build a query profile from a saved connection"
            : "Profile creation is unavailable"
        }
        onClick={() => setOpen(true)}
      >
        <Icon icon={UiMagicWand} className="size-4" />
        Build Profile
      </Button>

      {open && createAction ? (
        <ProfileBuilderAutoOpen>
          <SchemaActionForm
            client={client}
            action={createAction}
            initialValue={initialValue}
            submitLabel="Save Profile"
            formPost={profileBuilderFormExtensions.post}
            onSuccess={async () => {
              setOpen(false);
              await Promise.all([
                queryClient.invalidateQueries({ queryKey: ["openapi-spec"] }),
                queryClient.invalidateQueries({
                  queryKey: ["operation-list"],
                }),
                queryClient.invalidateQueries({
                  queryKey: ["logs-entity-names"],
                }),
              ]);
            }}
            renderLayout={({ body, footer }) => (
              <Modal
                open
                onClose={() => setOpen(false)}
                title="Build Profile"
                size="lg"
                {...(footer ? { footer } : {})}
              >
                {body}
              </Modal>
            )}
            fallback={
              <div className="text-sm text-muted-foreground">
                The profile form is unavailable.
              </div>
            }
          />
        </ProfileBuilderAutoOpen>
      ) : null}
    </>
  );
}
