import type { ResolvedOperation } from "@flanksource/clicky-ui";

export function buildProfileInitialValue(
  connectionName?: string,
  providerType?: string,
  providerOptions?: Record<string, unknown>,
  profileQuery?: string,
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
    ...(profileQuery ? { query: profileQuery } : {}),
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
