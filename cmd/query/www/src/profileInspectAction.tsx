import {
  Button,
  Icon,
  OperationActionDialog,
  useOperations,
  type OperationsApiClient,
} from "@flanksource/clicky-ui";
import { UiScan } from "@flanksource/clicky-ui/icons";
import { profileForSurface } from "./reconcileModel";
import { useProfiles } from "./profilesQuery";
import {
  findProfileInspectOperation,
  profileInspectInitialValues,
} from "./profileInspectOperation";

export function ProfileInspectButton({
  client,
  surfaceKey,
}: {
  client: OperationsApiClient;
  surfaceKey: string;
}) {
  const { operations, isLoading: operationsLoading } = useOperations(client);
  const profiles = useProfiles();
  const operation = findProfileInspectOperation(operations);
  const profile = profileForSurface(profiles.data ?? [], surfaceKey);

  if (!operation || !profile?.profile) {
    return (
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled
        title={
          operationsLoading || profiles.isLoading
            ? "Loading profile inspection"
            : "Profile inspection is unavailable"
        }
      >
        <Icon icon={UiScan} className="size-4" />
        Inspect
      </Button>
    );
  }

  return (
    <OperationActionDialog
      client={client}
      operation={operation}
      initialValues={profileInspectInitialValues(operation, profile)}
      label="Inspect"
    />
  );
}
