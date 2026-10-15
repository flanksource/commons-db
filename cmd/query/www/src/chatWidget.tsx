import { mountPath } from "./mount";
import type { ChatLayerProps } from "@flanksource/clicky-ui";

/** Query-specific chat policy; clicky-ui owns operation discovery and windows. */
export const queryChatConfig = {
  title: "Query Assistant",
  sessionsApi: `${mountPath}/api/chat/sessions`,
  chat: {
    api: `${mountPath}/api/chat`,
    modelsApi: `${mountPath}/api/chat/models`,
    suggestions: [
      "List configured connections",
      "Show available query profiles",
      "Summarize the available data sources",
    ],
    placeholder: "Ask about connections, profiles, or query results…",
  },
} satisfies Omit<ChatLayerProps, "client" | "operationFilter">;
