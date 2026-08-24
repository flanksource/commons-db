import type { ResolvedOperation } from "@flanksource/clicky-ui";

const NON_CHAT_OPERATIONS = new Set(["serve", "schema"]);

export function isQueryChatOperation(operation: ResolvedOperation): boolean {
  const id = operation.operation.operationId?.trim().toLowerCase();
  return id !== undefined && !NON_CHAT_OPERATIONS.has(id);
}
