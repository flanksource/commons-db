import {
  Button,
  Icon,
  useOperations,
  type OperationsApiClient,
} from "@flanksource/clicky-ui";
import { UiMagicWand } from "@flanksource/clicky-ui/icons";
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { ProfileWizard } from "@flanksource/clicky-ui/profiles";
import {
  buildProfileInitialValue,
  findProfileCreateOperation,
} from "./profileCreateOperation";

type BuildProfileButtonProps = {
  client: OperationsApiClient;
  connectionName?: string;
  providerType?: string;
  providerOptions?: Record<string, unknown>;
  profileQuery?: string;
};

export function BuildProfileButton({
  client,
  connectionName,
  providerType,
  providerOptions,
  profileQuery,
}: BuildProfileButtonProps) {
  const queryClient = useQueryClient();
  const { operations, isLoading } = useOperations(client);
  const createAction = findProfileCreateOperation(operations);
  const [open, setOpen] = useState(false);
  const initialValue = useMemo<Record<string, unknown>>(
    () => buildProfileInitialValue(connectionName, providerType, providerOptions, profileQuery),
    [connectionName, profileQuery, providerOptions, providerType],
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
        <ProfileWizard
          client={client}
          action={createAction}
          initialValue={initialValue}
          onClose={() => setOpen(false)}
          onSuccess={async () => {
            setOpen(false);
            await Promise.all([
              queryClient.invalidateQueries({ queryKey: ["openapi-spec"] }),
              queryClient.invalidateQueries({ queryKey: ["operation-list"] }),
              queryClient.invalidateQueries({ queryKey: ["logs-entity-names"] }),
            ]);
          }}
        />
      ) : null}
    </>
  );
}
