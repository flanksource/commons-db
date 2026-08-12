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
});
