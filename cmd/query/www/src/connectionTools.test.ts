import { describe, expect, it } from "vitest";
import { connectionToolSelection } from "./connectionTools";

describe("connection tool selection", () => {
  it.each([
    ["k8s", null, ["logs", "browser"], "logs"],
    ["k8s", "", ["logs", "browser"], "logs"],
    ["k8s", "browser", ["logs", "browser"], "browser"],
    ["opentelemetry", null, ["browser", "traces"], "traces"],
    ["opentelemetry", "browser", ["browser", "traces"], "browser"],
    ["opensearch", null, ["browser", "traces"], "browser"],
    ["opensearch", "traces", ["browser", "traces"], "traces"],
    ["sqlserver", "trace", ["browser", "trace"], "trace"],
    ["sqlserver", "traces", ["browser", "trace"], "browser"],
    ["k8s", "trace", ["logs", "browser"], "browser"],
    ["opentelemetry", "invalid", ["browser", "traces"], "browser"],
    [undefined, "logs", ["browser"], "browser"],
  ])("selects %s tools for tab %s", (provider, requested, tabs, activeTab) => {
    expect(connectionToolSelection(provider, requested)).toEqual({ tabs, activeTab });
  });
});
