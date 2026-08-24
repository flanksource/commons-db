/** @vitest-environment jsdom */

import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const reactRoot = vi.hoisted(() => ({
  create: vi.fn(),
  render: vi.fn(),
}));

vi.mock("react-dom/client", () => ({ createRoot: reactRoot.create }));
vi.mock("./App", () => ({ App: () => null, formExtensions: [] }));
vi.mock("./iconProvider", () => ({ FlanksourceIcon: () => null }));
vi.mock("@flanksource/clicky-ui", () => ({
  ErrorWrapper: function ErrorWrapper() {
    return null;
  },
  setFallbackIconProvider: vi.fn(),
}));
vi.mock("@flanksource/clicky-ui/profiles", () => ({
  configureProfiles: vi.fn(),
}));
vi.mock("@flanksource/clicky-ui/icons", () => ({
  clickyIconProvider: vi.fn(),
}));
vi.mock("react-grab", () => ({ registerPlugin: vi.fn() }));
vi.mock("./styles.css", () => ({}));
vi.mock("@flanksource/clicky-ui/styles.css", () => ({}));

describe("query web runtime", () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="root"></div>';
    reactRoot.create.mockReturnValue({ render: reactRoot.render });
  });

  it("mounts the application beneath the full-page error wrapper", async () => {
    await import("./main");

    expect(reactRoot.create).toHaveBeenCalledWith(
      document.getElementById("root"),
    );
    const mounted = reactRoot.render.mock.calls[0]?.[0] as ReactElement;
    expect(mounted.type).toHaveProperty("name", "ErrorWrapper");
    const strictMode = (mounted.props as { children: ReactElement }).children;
    const app = (strictMode.props as { children: ReactElement }).children;
    expect(app.type).toHaveProperty("name", "App");
  });
});
