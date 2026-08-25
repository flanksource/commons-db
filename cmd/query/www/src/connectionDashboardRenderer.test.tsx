import type { ResultRenderContext } from "@flanksource/clicky-ui";
import { isValidElement } from "react";
import { describe, expect, it } from "vitest";
import { connectionDashboardResultRenderer } from "./connectionDashboardRenderer";

function context(overrides: Partial<ResultRenderContext>): ResultRenderContext {
  return {
    surfaceKey: "connection",
    defaultView: null,
    response: { requestUrl: "/api/v1/connection?type=postgres&limit=50" },
    ...overrides,
  } as ResultRenderContext;
}

describe("connection dashboard renderer", () => {
  it("keeps the same surface identity when only the response body changes", () => {
    const first = connectionDashboardResultRenderer(
      context({ response: { requestUrl: "/api/v1/connection?type=postgres", output: "a" } as never }),
    );
    const second = connectionDashboardResultRenderer(
      context({ response: { requestUrl: "/api/v1/connection?type=postgres", output: "b" } as never }),
    );

    expect(isValidElement(first) && isValidElement(second)).toBe(true);
    // A response-derived key would mint a fresh react-query cache entry on
    // nearly every render, refetching the whole fleet each time.
    expect(isValidElement(first) ? first.props : null).toEqual(
      isValidElement(second) ? second.props : undefined,
    );
  });

  it("leaves surfaces other than connections to the default view", () => {
    const defaultView = "default";

    expect(
      connectionDashboardResultRenderer(context({ surfaceKey: "profile", defaultView })),
    ).toBe(defaultView);
  });
});
