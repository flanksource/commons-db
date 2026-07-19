import { useMemo } from "react";
import {
  useOperations,
  type OperationsApiClient,
  type ResolvedOperation,
} from "@flanksource/clicky-ui";
import {
  ChatFab,
  ChatWindowLayer,
} from "@flanksource/clicky-ui/ai";
import { clickyOperationsToTools } from "@flanksource/clicky-ui/chat";

const NON_CHAT_OPERATIONS = new Set(["serve", "schema"]);

export function isQueryChatOperation(operation: ResolvedOperation): boolean {
  const id = operation.operation.operationId?.trim().toLowerCase();
  return id !== undefined && !NON_CHAT_OPERATIONS.has(id);
}

/** Floating Clicky chat whose tool catalog is derived from the exact OpenAPI
 * operations used by EntityExplorerApp. Tool execution stays in the Go
 * backend; this component owns only discovery, preferences, and chat UX. */
export function ChatWidget({ client }: { client: OperationsApiClient }) {
  const { operations } = useOperations(client);
  const tools = useMemo(
    () => clickyOperationsToTools(operations.filter(isQueryChatOperation)),
    [operations],
  );

  return (
    <>
      <ChatFab label="Open query assistant" />
      <ChatWindowLayer
        title="Query Assistant"
        threadsApi="/api/chat/threads"
        tools={tools}
        chat={{
          api: "/api/chat",
          modelsApi: "/api/chat/models",
          suggestions: [
            "List configured connections",
            "Show available query profiles",
            "Summarize the available data sources",
          ],
          placeholder: "Ask about connections, profiles, or query results…",
        }}
      />
    </>
  );
}
