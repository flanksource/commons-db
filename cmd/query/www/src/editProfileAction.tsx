import {
  Button,
  Icon,
  useOperations,
  useRouter,
  type OperationsApiClient,
  type ResolvedOperation,
} from "@flanksource/clicky-ui";
import { UiEdit } from "@flanksource/clicky-ui/icons";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { ProfileEditor } from "./profileEditor";
import { profileEditRoute, profileRoute } from "./profileEditorModel";
import { PROFILES_QUERY_KEY } from "./profilesQuery";

export function isProfileSurface(surfaceKey?: string): boolean {
  return Boolean(surfaceKey?.startsWith("profile-") && surfaceKey !== "profiles");
}

export function findProfileUpdateOperation(
  operations: ResolvedOperation[],
): ResolvedOperation | undefined {
  return operations.find((operation) => {
    const metadata = operation.operation["x-clicky"];
    return (
      metadata?.surface === "profiles" &&
      metadata.scope === "entity" &&
      metadata.verb === "update"
    );
  });
}

export function findProfileGetOperation(
  operations: ResolvedOperation[],
): ResolvedOperation | undefined {
  return operations.find((operation) => {
    const metadata = operation.operation["x-clicky"];
    return (
      metadata?.surface === "profiles" &&
      metadata.scope === "entity" &&
      metadata.verb === "get"
    );
  });
}

/** Opens the editor route, so the edit surface is deep-linkable and survives a
 *  refresh — it used to be a dialog with no address of its own. */
export function EditProfileButton({
  client,
  surfaceKey,
}: {
  client: OperationsApiClient;
  surfaceKey: string;
}) {
  const router = useRouter();
  const { operations, isLoading } = useOperations(client);
  const available = Boolean(
    findProfileGetOperation(operations) && findProfileUpdateOperation(operations),
  );

  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      disabled={isLoading || !available}
      title={available ? "Edit this profile" : "Profile editing is unavailable"}
      onClick={() => router.navigate(profileEditRoute(surfaceKey))}
    >
      <Icon icon={UiEdit} className="size-4" />
      Edit
    </Button>
  );
}

/** The `/profile-<name>/edit` route: loads the profile document, then hands it
 *  to the editor workspace. */
export function ProfileEditorPage({
  client,
  surfaceKey,
}: {
  client: OperationsApiClient;
  surfaceKey: string;
}) {
  const queryClient = useQueryClient();
  const router = useRouter();
  const { operations, isLoading } = useOperations(client);
  const updateAction = findProfileUpdateOperation(operations);
  const getAction = findProfileGetOperation(operations);
  const profile = useQuery({
    queryKey: ["profile-editor", surfaceKey],
    enabled: getAction != null,
    queryFn: async () => {
      if (!getAction) throw new Error("Profile lookup operation is unavailable");
      const response = await client.executeCommand(
        getAction.path,
        getAction.method,
        { id: surfaceKey },
        { Accept: "application/json" },
      );
      if (!isRecord(response.parsed)) {
        throw new Error(`Profile ${surfaceKey} returned an invalid document`);
      }
      return response.parsed;
    },
    retry: 0,
  });

  const close = () => router.navigate(`/${surfaceKey}`);

  if (isLoading || profile.isLoading) {
    return <ProfileEditorMessage>Loading profile…</ProfileEditorMessage>;
  }
  if (profile.isError || !updateAction || !profile.data) {
    return (
      <ProfileEditorMessage error>
        {profile.error instanceof Error
          ? profile.error.message
          : "Profile editing is unavailable"}
      </ProfileEditorMessage>
    );
  }

  return (
    <ProfileEditor
      client={client}
      action={updateAction}
      surfaceKey={surfaceKey}
      initialValue={profile.data}
      onClose={close}
      onSuccess={async (name) => {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["openapi-spec"] }),
          queryClient.invalidateQueries({ queryKey: ["operation-list"] }),
          queryClient.invalidateQueries({ queryKey: ["profile-editor", surfaceKey] }),
          queryClient.invalidateQueries({ queryKey: ["profile-json-fields"] }),
          // The profile list drives the sidebar tree, the logs renderer and every
          // profile picker, so a rename that skipped it left them all stale.
          queryClient.invalidateQueries({ queryKey: PROFILES_QUERY_KEY }),
        ]);
        router.navigate(profileRoute(name), { replace: true });
      }}
    />
  );
}

function ProfileEditorMessage({
  children,
  error = false,
}: {
  children: ReactNode;
  error?: boolean;
}) {
  return (
    <div
      className={`grid h-full place-items-center p-8 text-sm ${error ? "text-destructive" : "text-muted-foreground"}`}
    >
      {children}
    </div>
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
