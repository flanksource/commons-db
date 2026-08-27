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

  // The default view carries the sort as one control; the table takes the value
  // and the setter as separate props. Handing the control straight through is
  // what left every server-paged profile table's headers inert.
  it("splits the default view's sort control into the table's two props", () => {
    const onChange = () => undefined;
    const props = profileRowDetailsRemoteProps(
      { requestUrl: "/api/v1/profile/profile-orders" },
      createElement("div", { sort: { value: { key: "amount", dir: "desc" }, onChange } }),
    );

    expect(props.sort).toEqual({ key: "amount", dir: "desc" });
    expect(props.onSortChange).toBe(onChange);
  });

  it("offers no sort when the default view has no sort control", () => {
    const props = profileRowDetailsRemoteProps(
      { requestUrl: "/api/v1/profile/profile-orders" },
      createElement("div", {}),
    );

    expect(props).not.toHaveProperty("sort");
    expect(props).not.toHaveProperty("onSortChange");
  });

  // The rows handed to the table came from this URL's clicky representation.
  // Saying so is what stops the table fetching the same payload a second time
  // to render bytes it already holds.
  it("declares rows served as clicky as the payload for that URL", () => {
    const requestUrl = "/api/v1/profile/profile-orders";

    expect(
      profileRowDetailsRemoteProps(
        { requestUrl, contentType: "application/json+clicky" },
        createElement("div"),
      ),
    ).toEqual({ url: requestUrl, dataFormat: "clicky" });
  });

  it("leaves rows from any other representation to be refreshed", () => {
    const requestUrl = "/api/v1/profile/profile-orders";

    expect(
      profileRowDetailsRemoteProps(
        { requestUrl, contentType: "application/json" },
        createElement("div"),
      ),
    ).toEqual({ url: requestUrl });
  });
});
