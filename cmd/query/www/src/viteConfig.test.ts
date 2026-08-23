import { describe, expect, it } from "vitest";
import { clickyUIDevAliases } from "../vite.config";

describe("clicky-ui development resolution", () => {
  it("uses sibling source only for the Vite development server", () => {
    expect(clickyUIDevAliases("build")).toEqual([]);

    expect(clickyUIDevAliases("serve")).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          find: "@flanksource/clicky-ui/styles.css",
          replacement: expect.stringContaining("/clicky-ui/packages/ui/src/styles/full.css"),
        }),
        expect.objectContaining({
          find: "@flanksource/clicky-ui",
          replacement: expect.stringContaining("/clicky-ui/packages/ui/src/index.ts"),
        }),
      ]),
    );
  });

  // A sub-path missing from the list resolves to the published dist instead of
  // the sibling source, so a dev-server edit to it silently does nothing.
  it("maps every sub-path the app imports", () => {
    const aliases = clickyUIDevAliases("serve");

    for (const entrypoint of ["ai", "data", "devtools", "monaco", "profiles", "rpc"]) {
      expect(aliases).toContainEqual({
        find: `@flanksource/clicky-ui/${entrypoint}`,
        replacement: expect.stringContaining(`/clicky-ui/packages/ui/src/${entrypoint}.ts`),
      });
    }
  });
});
