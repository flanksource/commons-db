import {
  Button,
  Icon,
  useOperations,
  useRouter,
  type OperationsApiClient,
} from "@flanksource/clicky-ui";
import type { DataTableMenuAction } from "@flanksource/clicky-ui/data";
import { UiEdit } from "@flanksource/clicky-ui/icons";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import {
  ProfileEditor,
  profileEditRoute,
  profileRoute,
} from "@flanksource/clicky-ui/profiles";
import { loadProfileDocument } from "./profileColumnPromotion";
import { PROFILES_QUERY_KEY } from "./profilesQuery";
import { findProfileUpdateOperation } from "./profileUpdateOperation";

/** Opens the editor route, so the edit surface is deep-linkable and survives a
 *  refresh — it used to be a dialog with no address of its own. A pencil beside
 *  the profile's title in the top bar. */
export function EditProfileButton({
  client,
  surfaceKey,
}: {
  client: OperationsApiClient;
  surfaceKey: string;
}) {
  const router = useRouter();
  const { operations, isLoading } = useOperations(client);
  const available = Boolean(findProfileUpdateOperation(operations));
  const label = available ? "Edit profile" : "Profile editing is unavailable";

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className="size-7"
      disabled={isLoading || !available}
      aria-label={label}
      title={label}
      onClick={() => router.navigate(profileEditRoute(surfaceKey))}
    >
      <Icon icon={UiEdit} className="size-4" />
    </Button>
  );
}

/** The same entry as a row in a profile result table's overflow menu. */
export function useEditProfileMenuAction(
  client: OperationsApiClient,
): (surfaceKey: string) => DataTableMenuAction {
  const router = useRouter();
  const { operations, isLoading } = useOperations(client);
  const available = Boolean(findProfileUpdateOperation(operations));
  return (surfaceKey) => ({
    id: "edit-profile",
    label: "Edit profile",
    icon: UiEdit,
    section: "",
    disabled: isLoading || !available,
    onSelect: () => router.navigate(profileEditRoute(surfaceKey)),
  });
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
  const profile = useQuery({
    queryKey: ["profile-editor", surfaceKey],
    queryFn: () => loadProfileDocument(surfaceKey),
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
