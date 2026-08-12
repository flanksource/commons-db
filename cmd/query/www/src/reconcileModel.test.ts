import { describe, expect, it } from "vitest";

import {
  boundWarning,
  celForPairings,
  deriveStats,
  displayKey,
  filterFlagValue,
  formatTime,
  findReconcileMaterializeAction,
  findProfileRunOperation,
  groupByKey,
  humanizeDuration,
  initialReconcileFilters,
  isDuplicated,
  laneGroups,
  lanePage,
  LANE_PAGE_SIZE,
  parseReconcileQuery,
  profileFields,
  profileForSurface,
  profileSlug,
  reconcileQueryString,
  reconcileRoute,
  reconcileSurfaceKey,
  reconcileFilterParameters,
  storedConfig,
  timestampField,
  type ProfileDocument,
  type ReconcileResult,
  type ReconcileRow,
} from "./reconcileModel";

const sourceProfile: ProfileDocument = {
  profile: "orders-emitted",
  columns: [
    { name: "order_id" },
    { name: "customer" },
    { name: "created_at", kind: "timestamp" },
  ],
  params: [{ name: "since" }, { name: "customer" }],
};

const destProfile: ProfileDocument = {
  profile: "orders-ingested",
  columns: [{ name: "order_ref" }, { name: "tenant" }, { name: "@timestamp", kind: "timestamp" }],
  params: [{ name: "since" }, { name: "service" }],
};

function matched(key: string, diffNs: number): ReconcileRow {
  return {
    key,
    status: "matched",
    source: { order_id: key },
    dest: { order_ref: key },
    source_time: "2026-08-05T09:14:02.100Z",
    dest_time: "2026-08-05T09:14:02.520Z",
    time_diff: diffNs,
  };
}

/** The 2×2 expansion the engine emits when one identity repeats on both sides. */
function duplicatedPairs(key: string): ReconcileRow[] {
  const rows: ReconcileRow[] = [];
  for (let source = 1; source <= 2; source++) {
    for (let dest = 1; dest <= 2; dest++) {
      rows.push({
        ...matched(key, 480_000_000),
        source_dup_index: source,
        source_dup_count: 2,
        dest_dup_index: dest,
        dest_dup_count: 2,
      });
    }
  }
  return rows;
}

const rows: ReconcileRow[] = [
  matched("1041", 420_000_000),
  matched("1042", 1_200_000_000),
  ...duplicatedPairs("1049"),
  { key: "1046", status: "only_source", source: { order_id: "1046" }, source_time: "2026-08-05T09:14:11.250Z" },
  { key: "1051", status: "only_dest", dest: { order_ref: "1051" }, dest_time: "2026-08-05T09:14:26.400Z" },
  { key: "", status: "only_dest", dest: { order_ref: "" } },
];

describe("the reconcile route", () => {
  it("sits beside the profile's own edit route", () => {
    expect(reconcileRoute("profile-orders-emitted")).toBe("/profile-orders-emitted/reconcile");
  });

  it("recognises its own path and nothing else", () => {
    expect(reconcileSurfaceKey("/profile-orders-emitted/reconcile")).toBe("profile-orders-emitted");
    expect(reconcileSurfaceKey("/profile-orders-emitted/reconcile/")).toBe("profile-orders-emitted");
    expect(reconcileSurfaceKey("/profile-orders-emitted/edit")).toBeNull();
    expect(reconcileSurfaceKey("/connections/reconcile")).toBeNull();
  });

  it("round-trips the bench state through the query string", () => {
    const query = {
      dest: "orders-ingested",
      cel: "row.id",
      snapshotAge: "30m",
      sourceFilters: { region: "eu", "filter.tenant": "acme,blue" },
      destFilters: { region: "us" },
    };
    expect(parseReconcileQuery(reconcileQueryString(query))).toEqual(query);
  });

  it("drops stored destination filters when the URL chooses another destination", () => {
    expect(
      initialReconcileFilters(
        { dest: "old-dest", sourceFilters: { region: "eu" }, destFilters: { tenant: "old" } },
        { dest: "new-dest", destFilters: { tenant: "new" } },
      ),
    ).toEqual({ sourceFilters: { region: "eu" }, destFilters: { tenant: "new" } });
  });

});

describe("reconcile actions", () => {
  const operation = (actionName: string) => ({
    path: `/api/v1/profiles/{id}/${actionName}`,
    method: "post",
    operation: {
      responses: {},
      "x-clicky": { surface: "profiles", actionName, verb: "action" as const, scope: "entity" as const },
    },
  });

  it("finds the snapshot materialization sub-action", () => {
    expect(
      findReconcileMaterializeAction([
        operation("reconcile"),
        operation("reconcile-materialize"),
      ]),
    ).toMatchObject({ path: "/api/v1/profiles/{id}/reconcile-materialize" });
  });

  it("finds the destination's normal run operation and excludes transport controls", () => {
    const run = {
      path: "/api/v1/profile/profile-orders-ingested",
      method: "get",
      operation: {
        responses: {},
        parameters: [
          { name: "region", in: "query" as const, "x-clicky": { role: "filter" as const } },
          { name: "from", in: "query" as const, "x-clicky": { role: "time-from" as const } },
          { name: "limit", in: "query" as const, "x-clicky": { role: "limit" as const } },
          { name: "cursor", in: "query" as const, "x-clicky": { role: "cursor" as const } },
        ],
        "x-clicky": { surface: "profile-orders-ingested", verb: "list" as const, scope: "collection" as const },
      },
    };

    expect(findProfileRunOperation([run], "Orders Ingested")).toBe(run);
    expect(reconcileFilterParameters(run).map((parameter) => parameter.name)).toEqual(["region", "from"]);
  });

  it("CSV-encodes sorted repeatable filters without splitting list values", () => {
    expect(filterFlagValue({ region: "eu", "filter.tenant": 'acme,"blue"' })).toBe(
      '"filter.tenant=acme,""blue""",region=eu',
    );
  });
});

describe("addressing a profile", () => {
  it("mirrors the store's slug, which is what the route carries", () => {
    expect(profileSlug("Orders Emitted")).toBe("orders-emitted");
    expect(profileSlug("jms.outgoing")).toBe("jms-outgoing");
    expect(profileSlug("  SQL Users  ")).toBe("sql-users");
  });

  it("maps a surface key back to the stored profile, whose name the API wants", () => {
    const documents = [sourceProfile, destProfile];
    expect(profileForSurface(documents, "profile-orders-emitted")?.profile).toBe("orders-emitted");
    expect(profileForSurface(documents, "profile-nothing")).toBeUndefined();
  });
});

describe("celForPairings", () => {
  it("branches on which side's field is present, so one expression serves both", () => {
    expect(celForPairings([{ source: "order_id", dest: "order_ref" }])).toBe(
      "has(row.order_id) ? string(row.order_id) : string(row.order_ref)",
    );
  });

  it("parenthesises a multi-part key, since a conditional binds looser than concatenation", () => {
    expect(
      celForPairings([
        { source: "order_id", dest: "order_ref" },
        { source: "customer", dest: "tenant" },
      ]),
    ).toBe(
      '(has(row.order_id) ? string(row.order_id) : string(row.order_ref)) + "\\x00" + ' +
        "(has(row.customer) ? string(row.customer) : string(row.tenant))",
    );
  });

  it("is empty until something is paired", () => {
    expect(celForPairings([])).toBe("");
  });
});

describe("reading a profile document", () => {
  it("offers its declared columns as key candidates", () => {
    expect(profileFields(sourceProfile)).toEqual(["order_id", "customer", "created_at"]);
    expect(profileFields(undefined)).toEqual([]);
  });

  it("finds the column the engine would take the event time from", () => {
    expect(timestampField(sourceProfile)).toBe("created_at");
    expect(timestampField(destProfile)).toBe("@timestamp");
    expect(timestampField({ columns: [{ name: "id" }] })).toBe("");
  });
});

describe("deriveStats", () => {
  it("counts per key, not per emitted row", () => {
    expect(deriveStats(rows)).toEqual({ matched: 3, only_source: 1, only_dest: 2, dup_keys: 1 });
  });

  it("counts a duplicated key once while it emits every pair", () => {
    const duplicated = duplicatedPairs("1049");
    expect(duplicated).toHaveLength(4);
    expect(deriveStats(duplicated)).toEqual({ matched: 1, only_source: 0, only_dest: 0, dup_keys: 1 });
  });
});

describe("grouping and lanes", () => {
  const groups = groupByKey(rows);

  it("collapses the cartesian expansion into one group carrying both dup counts", () => {
    const group = groups.find((entry) => entry.key === "1049");
    expect(group).toMatchObject({ status: "matched", sourceDupCount: 2, destDupCount: 2 });
    expect(group?.pairs).toHaveLength(4);
    expect(isDuplicated(group!)).toBe(true);
  });

  it("keeps a missing side at a dup count of zero", () => {
    expect(groups.find((entry) => entry.key === "1046")).toMatchObject({
      sourceDupCount: 1,
      destDupCount: 0,
    });
  });

  it("gives each lane the keys that belong in it", () => {
    expect(laneGroups(groups, "only_source").map((group) => group.key)).toEqual(["1046"]);
    expect(laneGroups(groups, "only_dest").map((group) => group.key)).toEqual(["1051", ""]);
    expect(laneGroups(groups, "ambiguous").map((group) => group.key)).toEqual(["1049"]);
    expect(laneGroups(groups, "matched")).toHaveLength(3);
  });
});

describe("lanePage", () => {
  const keys = (count: number) =>
    Array.from({ length: count }, (_, index) => ({ key: `k${index}` })) as ReturnType<
      typeof groupByKey
    >;

  // A lane the browser stops painting has quietly hidden the rest, which is the
  // same lie a truncated read tells — so the window is explicit and counted.
  it("windows a lane and reports how many keys it has", () => {
    const page = lanePage(keys(250), 1, 100);
    expect(page.groups).toHaveLength(100);
    expect(page.groups[0]?.key).toBe("k100");
    expect(page).toMatchObject({ page: 1, pages: 3, total: 250 });
  });

  it("keeps a short lane on one page", () => {
    expect(lanePage(keys(4), 0, 100)).toMatchObject({ page: 0, pages: 1, total: 4 });
  });

  // Landing on an empty table reads as an empty lane, so an out-of-range page
  // shows the last one instead.
  it("clamps a page past the end onto the last one", () => {
    const page = lanePage(keys(250), 99, 100);
    expect(page.page).toBe(2);
    expect(page.groups).toHaveLength(50);
  });

  it("defaults to the lane page size", () => {
    expect(lanePage(keys(LANE_PAGE_SIZE + 1), 0).groups).toHaveLength(LANE_PAGE_SIZE);
  });
});

describe("humanizeDuration", () => {
  it.each([
    [0, "0s"],
    [640_000_000, "640ms"],
    [-900_000_000, "-900ms"],
    [7_400_000_000, "7.40s"],
    [90_000_000_000, "1m30s"],
    [1_500, "1µs"],
    [420, "420ns"],
  ])("formats %ins as %s, matching the Go renderer", (ns, expected) => {
    expect(humanizeDuration(ns)).toBe(expected);
  });
});

describe("formatTime", () => {
  it("prints the CLI's time-of-day format in UTC", () => {
    expect(formatTime("2026-08-05T09:14:02.520Z")).toBe("09:14:02.520");
  });

  it("is blank for a missing or unparseable timestamp", () => {
    expect(formatTime(undefined)).toBe("");
    expect(formatTime("not a time")).toBe("");
  });
});

describe("displayKey", () => {
  it("names the empty key rather than rendering a blank cell", () => {
    expect(displayKey("")).toBe("(empty)");
    expect(displayKey("1041")).toBe("1041");
  });
});

describe("boundWarning", () => {
  const result = (extra: Partial<ReconcileResult>): ReconcileResult => ({
    source: "a",
    dest: "b",
    rows: [],
    stats: { matched: 0, only_source: 0, only_dest: 0, dup_keys: 0 },
    ...extra,
  });

  it("says nothing when the run read both sides in full", () => {
    expect(boundWarning(result({ limit: 100 }))).toBe("");
  });

  it("names the side that was cut, because its misses are not findings", () => {
    expect(boundWarning(result({ limit: 50, source_truncated: true }))).toContain("the source was");
    expect(boundWarning(result({ limit: 50, dest_truncated: true }))).toContain("the destination was");
    expect(
      boundWarning(result({ limit: 50, source_truncated: true, dest_truncated: true })),
    ).toContain("both sides were");
  });
});

describe("storedConfig", () => {
  it("stores the key as CEL", () => {
    expect(storedConfig({ dest: "orders-ingested", cel: "row.id", sourceFilters: {}, destFilters: {} })).toEqual({
      dest: "orders-ingested",
      key: { cel: "row.id" },
    });
  });

  it("keeps filters when they are set", () => {
    expect(
      storedConfig({
        dest: "orders-ingested",
        cel: "row.id",
        sourceFilters: { since: "-15m" },
        destFilters: { tenant: "tenant-x" },
      }),
    ).toEqual({
      dest: "orders-ingested",
      key: { cel: "row.id" },
      sourceFilters: { since: "-15m" },
      destFilters: { tenant: "tenant-x" },
    });
  });
});
