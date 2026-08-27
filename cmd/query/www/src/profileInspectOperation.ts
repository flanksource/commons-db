import type { ResolvedOperation } from "@flanksource/clicky-ui";
import type { ProfileDocument } from "./reconcileModel";

export function findProfileInspectOperation(
  operations: ResolvedOperation[],
): ResolvedOperation | undefined {
  return operations.find((operation) => {
    const metadata = operation.operation["x-clicky"];
    return (
      metadata?.surface === "profiles" &&
      metadata.scope === "entity" &&
      metadata.actionName === "inspect"
    );
  });
}

export function profileInspectInitialValues(
  operation: ResolvedOperation,
  profile: ProfileDocument,
): Record<string, string> {
  const name = profile.profile?.trim();
  if (!name) throw new Error("profile name is required to inspect a profile");
  return { [operation.operation["x-clicky"]?.idParam ?? "id"]: name };
}
