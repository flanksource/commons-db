import {
  normalizeLogsTableRows,
  type LogsTableRow,
  type ResultRenderContext,
} from "@flanksource/clicky-ui";
import { isValidElement, type ComponentProps, type ReactElement } from "react";
import { describe, expect, it } from "vitest";
import {
  followErrorText,
  followGrouping,
  followRowId,
  followRunIndex,
  LogsSurface,
  LOGS_SORT,
  TAIL_MAX_ROWS,
} from "./logsFollow";
import {
  clickyLogsRows,
  clickyLogsWalk,
  followParams,
  logsColumnFilterKeys,
  logsProfileSurfaces,
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

// The page the cursor resumes onto: same shape, later timestamp, so a walk of
// the two is checkable against the order it claims to preserve.
const secondPage = {
  version: 1,
  node: {
    kind: "table",
    columns: clickyLogsPayload.node.columns,
    rows: [
      {
        cells: {
          timestamp: { kind: "text", plain: "2026-08-17T10:00:05Z" },
          message: { kind: "text", plain: "cycle started" },
          labels: {
            kind: "map",
            fields: [
              { name: "namespace", value: { kind: "text", plain: "tenant-x" } },
              { name: "pod", value: { kind: "text", plain: "cycle-1" } },
            ],
          },
        },
      },
    ],
  },
};

const LOGS_REQUEST_URL = "/api/v1/profiles/profile-kubernetes-pod-logs?limit=500";

function pageResponse(parsed: unknown, requestUrl = LOGS_REQUEST_URL) {
  return { success: true, exit_code: 0, stdout: "", requestUrl, parsed };
}

// The one logs surface every renderer test resolves against: the stored name the
// session endpoint needs, and a provider that can tail.
const K8S_SURFACES = new Map([
  ["profile-kubernetes-pod-logs", { profile: "Kubernetes Pod Logs", follow: true }],
]);

function surfaceElement(context: ResultRenderContext, surfaces = K8S_SURFACES) {
  return logsResultRenderer(surfaces)(context) as ReactElement<
    ComponentProps<typeof LogsSurface>
  >;
}

describe("logsProfileSurfaces", () => {
  it("keys each logs surface by its route slug and carries the stored profile name", () => {
    // The route says `profile-kubernetes-pod-logs`; POST /profile/{name}/sessions
    // resolves through the store, which only knows "Kubernetes Pod Logs".
    expect(
      logsProfileSurfaces([
        { profile: "Kubernetes Pod Logs", render: "logs", follow: true },
        { profile: "Audit Trail", render: "logs" },
        { profile: "Ledger", render: "table", follow: true },
      ]),
    ).toEqual(
      new Map([
        ["profile-kubernetes-pod-logs", { profile: "Kubernetes Pod Logs", follow: true }],
        ["profile-audit-trail", { profile: "Audit Trail", follow: false }],
      ]),
    );
  });
});

describe("followParams", () => {
  it("replays the walk's own parameters into the session and drops the paging ones", () => {
    // `limit` is the page the walk asked for, not something the profile declared;
    // sending it would cap a tail that is supposed to run until it is stopped.
    expect(
      followParams(
        "/api/v1/profiles/profile-kubernetes-pod-logs?limit=500&page=2&namespace=tenant-x&filter.Level=error",
      ),
    ).toEqual({ namespace: "tenant-x", "filter.Level": "error" });
  });

  it("returns no parameters for a request that carried none", () => {
    expect(followParams("/api/v1/profiles/profile-kubernetes-pod-logs")).toEqual({});
  });
});

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

  it("reads the whole value of a column rendered truncated to a display width", () => {
    // Shape taken from a live `Kubernetes Pod Logs` page: a column with a width
    // renders its value truncated into `plain` and keeps the whole of it in the
    // children it was rendered from. Taking `plain` hands LogsTable
    // "2026-08-18 …", which is not a date it can parse, so the Time column
    // renders empty rather than narrow.
    expect(
      clickyLogsRows({
        node: {
          kind: "table",
          columns: [
            { name: "timestamp", label: "Time" },
            { name: "count", label: "Seen" },
          ],
          rows: [
            {
              cells: {
                timestamp: {
                  kind: "text",
                  plain: "2026-08-18 …",
                  style: { className: "max-w-[12ch] truncate", maxWidth: 12 },
                  children: [
                    { kind: "text", plain: "2026-08-18 07:39:59", text: "2026-08-18 07:39:59" },
                  ],
                },
                count: {
                  kind: "text",
                  plain: "1.…",
                  style: { className: "max-w-[4ch] truncate", maxWidth: 4 },
                  children: [{ kind: "text", plain: "1.00", text: "1.00" }],
                },
              },
            },
          ],
        },
      }),
    ).toEqual([{ timestamp: "2026-08-18 07:39:59", count: "1.00" }]);
  });
});

describe("logsResultRenderer", () => {
  it("renders the follow surface over the current page and forwards refetch loading", () => {
    const context: ResultRenderContext = {
      response: pageResponse(clickyLogsPayload),
      defaultView: "default view",
      loading: true,
    };

    const result = logsResultRenderer(K8S_SURFACES)(context);

    expect(isValidElement(result)).toBe(true);
    const element = result as ReactElement<ComponentProps<typeof LogsSurface>>;
    expect(element.type).toBe(LogsSurface);
    expect(element.props.history).toEqual(clickyLogsRows(clickyLogsPayload));
    expect(element.props.loading).toBe(true);
  });

  it("addresses the session by the stored profile name rather than the route slug", () => {
    const element = surfaceElement({
      response: pageResponse(clickyLogsPayload, `${LOGS_REQUEST_URL}&namespace=tenant-x`),
      defaultView: "default view",
      loading: false,
    });

    expect(element.props.profile).toBe("Kubernetes Pod Logs");
    expect(element.props.params).toEqual({ namespace: "tenant-x" });
    // Keyed by profile, so navigating between two logs surfaces cannot carry a
    // running session onto whichever log was opened next.
    expect(element.key).toBe("Kubernetes Pod Logs");
  });

  it("offers the toggle only where the profile's provider can tail its source", () => {
    const context: ResultRenderContext = {
      response: pageResponse(clickyLogsPayload),
      defaultView: "default view",
      loading: false,
    };

    expect(surfaceElement(context).props.canFollow).toBe(true);
    expect(
      surfaceElement(
        context,
        new Map([
          ["profile-kubernetes-pod-logs", { profile: "Kubernetes Pod Logs", follow: false }],
        ]),
      ).props.canFollow,
    ).toBe(false);
  });

  it("uses the canonical result view when a logs response is not a table", () => {
    const context: ResultRenderContext = {
      response: pageResponse(
        { node: { kind: "text", plain: "not a table" } },
        "/api/v1/profiles/profile-kubernetes-pod-logs",
      ),
      defaultView: "default view",
      loading: false,
    };

    expect(logsResultRenderer(K8S_SURFACES)(context)).toBe(context.defaultView);
  });

  it("renders every page of a cursor walk, oldest first, and forwards the load-more handle", () => {
    const infinite = { hasMore: true, loading: false, onLoadMore: () => {} };
    const element = surfaceElement({
      response: pageResponse(secondPage),
      pages: [pageResponse(clickyLogsPayload), pageResponse(secondPage)],
      infinite,
      defaultView: "default view",
      loading: false,
    });

    expect(element.props.history).toEqual([
      ...(clickyLogsRows(clickyLogsPayload) ?? []),
      ...(clickyLogsRows(secondPage) ?? []),
    ]);
    expect(element.props.infinite).toBe(infinite);
  });
});

describe("clickyLogsWalk", () => {
  it("concatenates the pages of a walk in the order they were fetched", () => {
    expect(
      clickyLogsWalk(
        [pageResponse(clickyLogsPayload), pageResponse(secondPage)],
        pageResponse(secondPage),
      ),
    ).toEqual([
      ...(clickyLogsRows(clickyLogsPayload) ?? []),
      ...(clickyLogsRows(secondPage) ?? []),
    ]);
  });

  it("reads a single-page walk straight off the response", () => {
    expect(
      clickyLogsWalk([pageResponse(clickyLogsPayload)], pageResponse(clickyLogsPayload)),
    ).toEqual(clickyLogsRows(clickyLogsPayload));
  });

  it("refuses the whole walk when a page in the middle carries no table", () => {
    // Skipping it would leave the rows either side looking consecutive, which is
    // the one failure a reader of an ordered log cannot detect.
    expect(
      clickyLogsWalk(
        [
          pageResponse(clickyLogsPayload),
          pageResponse({ node: { kind: "text", plain: "not a table" } }),
          pageResponse(secondPage),
        ],
        pageResponse(secondPage),
      ),
    ).toBeUndefined();
  });
});

/* ------------------------------------------------------- the follow surface */

const history = [
  { timestamp: "2026-08-17T10:00:00Z", message: "cycle completed" },
  { timestamp: "2026-08-17T10:00:05Z", message: "cycle started" },
];
// The same line the walk already holds, arriving again down the tail: the two
// runs may overlap as easily as they may leave a hole between them.
const tail = [
  { timestamp: "2026-08-17T10:00:05Z", message: "cycle started" },
  { timestamp: "2026-08-17T10:04:00Z", message: "reservation refused" },
];

/** The rows as LogsTable hands them back to `grouping` and `getRowId`. */
const rendered = normalizeLogsTableRows([...history, ...tail]);

describe("LOGS_SORT", () => {
  it("orders oldest first, which is the only order the walk can be resumed in", () => {
    // A partial walk re-sorted in the browser would order the pages fetched so
    // far against each other and present that as the order of the whole.
    expect(LOGS_SORT).toEqual({ key: "timestamp", dir: "asc" });
  });
});

describe("followRunIndex", () => {
  it("attributes every rendered row to the run that produced it", () => {
    const runs = followRunIndex(history, tail);
    expect(history.map((row) => runs.get(row))).toEqual(["history", "history"]);
    expect(tail.map((row) => runs.get(row))).toEqual(["tail", "tail"]);
  });
});

describe("followGrouping", () => {
  const grouping = followGrouping(followRunIndex(history, tail), {
    droppedRows: 0,
    startedAt: undefined,
  });

  it("splits the rendered rows at the moment the tail took over", () => {
    expect(rendered.map(grouping.getGroupKey)).toEqual([
      "history",
      "history",
      "tail",
      "tail",
    ]);
  });

  it("refuses a row it cannot attribute rather than filing it under the walk", () => {
    // Silently grouping an unknown row with the history is the failure the marker
    // exists to prevent: it would move the seam without moving what is above it.
    const [stranger] = normalizeLogsTableRows([{ timestamp: "2026-08-17T11:00:00Z" }]);
    expect(() => grouping.getGroupKey(stranger as LogsTableRow)).toThrow(
      /neither the loaded pages nor the live tail/,
    );
  });

  it("says the tail starts here and that earlier lines may be missing", () => {
    // The rows either side of the seam look consecutive, and nothing in an
    // ordered log tells a reader that the run skipped the time between them.
    expect(grouping.getGroupLabel?.("tail", [])).toMatch(/earlier lines may not be loaded/);
    expect(grouping.getGroupLabel?.("history", [])).toBe("Loaded pages");
  });

  it("reports the lines the tail's cap evicted at the point they are missing from", () => {
    const dropped = followGrouping(followRunIndex(history, tail), {
      droppedRows: 12,
      startedAt: undefined,
    });
    expect(String(dropped.getGroupMeta?.("tail", []))).toContain("12 earlier lines");
    expect(String(dropped.getGroupMeta?.("tail", []))).toContain(String(TAIL_MAX_ROWS));
    expect(grouping.getGroupMeta?.("tail", [])).toBeUndefined();
    expect(dropped.getGroupMeta?.("history", [])).toBeUndefined();
  });
});

describe("followRowId", () => {
  it("keys a line by its run, so one line in both runs stays two rows", () => {
    const rowId = followRowId(followRunIndex(history, tail));
    const ids = rendered.map(rowId);
    expect(new Set(ids).size).toBe(rendered.length);
    expect(ids[1]?.startsWith("history:")).toBe(true);
    expect(ids[2]?.startsWith("tail:")).toBe(true);
  });
});

describe("followErrorText", () => {
  it("names the session cap as the reason Follow is not running", () => {
    expect(
      followErrorText({
        scope: "start",
        httpStatus: 409,
        message: "the server is already running as many follow sessions as it allows (HTTP 409)",
      }),
    ).toBe(
      "Follow could not start: the server is already running as many follow sessions as it allows (HTTP 409)",
    );
  });

  it("separates a stream that broke from a session that never opened", () => {
    expect(followErrorText({ scope: "stream", message: "kubelet closed the stream" })).toBe(
      "The live tail reported an error: kubelet closed the stream",
    );
    expect(followErrorText({ scope: "session", message: "profile ended" })).toBe(
      "The follow session ended: profile ended",
    );
  });
});
