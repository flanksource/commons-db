import { createElement } from "react";
import { describe, expect, it } from "vitest";
import type {
  ClickyColumn,
  ClickyDownloadOptions,
  ClickyRow,
  ClickyTableProps,
} from "@flanksource/clicky-ui/clicky";
import {
  profileRowDetailNode,
  profileRowDetailTitle,
  profileRowDetailsRemoteProps,
} from "./profileRowDetails";

const row: ClickyRow = {
  cells: {
    operationName: { kind: "text", plain: "GET /health", text: "GET /health" },
    traceID: { kind: "text", plain: "trace-123", text: "trace-123" },
  },
};

const columns: ClickyColumn[] = [
  { name: "operationName", label: "Operation" },
];

describe("profile row details", () => {
  it("uses the operation name as the dialog title", () => {
    expect(profileRowDetailTitle(row)).toBe("GET /health details");
  });

  it("includes visible and hidden row fields in the detail map", () => {
    expect(profileRowDetailNode(row, columns)).toEqual({
      kind: "map",
      fields: [
        {
          name: "operationName",
          label: "Operation",
          value: row.cells.operationName,
        },
        {
          name: "traceID",
          label: "traceID",
          value: row.cells.traceID,
        },
      ],
    });
  });

  it("preserves the request URL, paging, and every advertised export option", () => {
    const download: ClickyDownloadOptions = {
      formats: ["json", "ndjson", "csv", "yaml", "markdown", "html", "excel", "pdf"],
      scopes: ["page", "all"],
      allRowsMode: "streaming",
      formatMaxRows: { pdf: 1000 },
    };
    const pagination: NonNullable<ClickyTableProps["pagination"]> = {
      page: 2,
      pageSize: 100,
      onPageChange: () => undefined,
      onPageSizeChange: () => undefined,
    };
    const requestUrl = "/api/v1/profile/profile-orders?limit=100&offset=100";

    expect(
      profileRowDetailsRemoteProps(
        { requestUrl },
        createElement("div", { download, pagination }),
      ),
    ).toEqual({ download, pagination, url: requestUrl });
  });
});
