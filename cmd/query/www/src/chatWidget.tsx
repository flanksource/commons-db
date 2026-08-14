import type { ChatLayerProps } from "@flanksource/clicky-ui";

/** Query-specific chat policy; clicky-ui owns operation discovery and windows. */
export const queryChatConfig = {
  title: "Query Assistant",
  sessionsApi: "/api/chat/sessions",
  chat: {
    api: "/api/chat",
    modelsApi: "/api/chat/models",
    suggestions: [
      "List configured connections",
      "Show available query profiles",
      "Summarize the available data sources",
    ],
    placeholder: "Ask about connections, profiles, or query results…",
  },
} satisfies Omit<ChatLayerProps, "client" | "operationFilter">;
