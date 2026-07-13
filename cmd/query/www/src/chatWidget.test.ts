import { describe, expect, it } from "vitest";
import type { ResolvedOperation } from "@flanksource/clicky-ui";
import { isQueryChatOperation } from "./chatWidget";

function operation(operationId?: string): ResolvedOperation {
  return {
    path: "/api/v1/example",
    method: "get",
    operation: {
      ...(operationId ? { operationId } : {}),
      responses: {},
    },
  };
}

describe("isQueryChatOperation", () => {
  it("keeps explorer data operations", () => {
    expect(isQueryChatOperation(operation("connection_get"))).toBe(true);
    expect(isQueryChatOperation(operation("profile-orders"))).toBe(true);
  });

  it("drops process and schema commands", () => {
    expect(isQueryChatOperation(operation("serve"))).toBe(false);
    expect(isQueryChatOperation(operation("schema"))).toBe(false);
  });

  it("drops operations without a stable tool id", () => {
    expect(isQueryChatOperation(operation())).toBe(false);
  });
});
