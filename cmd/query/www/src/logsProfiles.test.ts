import { LogsTable, type ResultRenderContext } from "@flanksource/clicky-ui";
import { isValidElement, type ComponentProps, type ReactElement } from "react";
import { describe, expect, it } from "vitest";
import {
  clickyLogsRows,
  logsColumnFilterKeys,
  logsResultRenderer,
} from "./logsProfiles";

const clickyLogsPayload = {
  version: 1,
  node: {
    kind: "table",
    columns: [
      { name: "timestamp", label: "Timestamp" },
      { name: "message", label: "Message" },
      { name: "labels", label: "Labels" },
    ],
    rows: [
      {
        cells: {
          timestamp: { kind: "text", plain: "2026-08-17T10:00:00Z" },
          message: { kind: "text", plain: "cycle completed" },
          labels: {
            kind: "map",
            fields: [
              { name: "namespace", value: { kind: "text", plain: "tenant-x" } },
              { name: "pod", value: { kind: "text", plain: "cycle-0" } },
            ],
          },
        },
      },
    ],
  },
};

describe("logsColumnFilterKeys", () => {
  it("maps Clicky table column names to native profile filter parameters", () => {
    expect(
      logsColumnFilterKeys({
        node: {
          kind: "table",
          columns: [
            { name: "level", label: "Level", filterKey: "filter.Level" },
            { name: "message", label: "Message" },
          ],
          rows: [],
        },
      }),
    ).toEqual({ level: "filter.Level" });
  });
});

describe("clickyLogsRows", () => {
  it("decodes the outer Clicky page into structured log records", () => {
    expect(clickyLogsRows(clickyLogsPayload)).toEqual([
      {
        timestamp: "2026-08-17T10:00:00Z",
        message: "cycle completed",
        labels: { namespace: "tenant-x", pod: "cycle-0" },
      },
    ]);
  });

  it("returns undefined when the Clicky result does not contain a table", () => {
    expect(clickyLogsRows({ node: { kind: "text", plain: "not a table" } })).toBeUndefined();
  });
});

describe("logsResultRenderer", () => {
  it("renders LogsTable directly from the current page and forwards refetch loading", () => {
    const defaultView = "default view";
    const context: ResultRenderContext = {
      response: {
        success: true,
        exit_code: 0,
        stdout: "",
        requestUrl: "/api/v1/profiles/profile-kubernetes-pod-logs?limit=500",
        parsed: clickyLogsPayload,
      },
      defaultView,
      loading: true,
    };

    const result = logsResultRenderer(new Set(["profile-kubernetes-pod-logs"]))(context);

    expect(isValidElement(result)).toBe(true);
    const element = result as ReactElement<ComponentProps<typeof LogsTable>>;
    expect(element.type).toBe(LogsTable);
    expect(element.props.logs).toEqual(clickyLogsRows(clickyLogsPayload));
    expect(element.props.loading).toBe(true);
  });

  it("uses the canonical result view when a logs response is not a table", () => {
    const context: ResultRenderContext = {
      response: {
        success: true,
        exit_code: 0,
        stdout: "",
        requestUrl: "/api/v1/profiles/profile-kubernetes-pod-logs",
        parsed: { node: { kind: "text", plain: "not a table" } },
      },
      defaultView: "default view",
      loading: false,
    };

    expect(logsResultRenderer(new Set(["profile-kubernetes-pod-logs"]))(context)).toBe(
      context.defaultView,
    );
  });
});
