import type { ResolvedOperation } from "@flanksource/clicky-ui";

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
