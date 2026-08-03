import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

describe("application stylesheet cascade", () => {
  it("loads app utilities before clicky-ui utilities under one layer order", () => {
    const main = readFileSync(new URL("./main.tsx", import.meta.url), "utf8");
    const styles = readFileSync(new URL("./styles.css", import.meta.url), "utf8");

    expect(main.indexOf('import "./styles.css"')).toBeLessThan(
      main.indexOf('import "@flanksource/clicky-ui/styles.css"'),
    );
    expect(styles.indexOf("@layer properties, theme, base, clicky-tokens, utilities;")).toBeLessThan(
      styles.indexOf('@import "tailwindcss/theme.css"'),
    );
  });
});
