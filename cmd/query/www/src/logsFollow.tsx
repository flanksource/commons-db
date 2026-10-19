import {
  Button,
  Icon,
  LogsTable,
  Switch,
  useLogTail,
  type ClickyDownloadOptions,
  type DataTableInfinite,
  type DataTablePagination,
  type LogTailError,
  type OperationResultFilterConfig,
} from "@flanksource/clicky-ui";
import { UiArrowDown, UiWarningTriangle } from "@flanksource/clicky-ui/icons";
import { useState, type ComponentProps } from "react";
import {
  followErrorText,
  followGrouping,
  followRowId,
  followRunIndex,
  LOGS_SORT,
  TAIL_MAX_ROWS,
} from "./logsFollowModel";
import { useFollowScroll } from "./useFollowScroll";

export interface LogsSurfaceProps {
  /** The cursor walk so far, oldest first. */
  history: Record<string, unknown>[];
  /** Stored profile name — what POST /profile/{name}/sessions resolves through the store. */
  profile: string;
  /** Whether this profile's provider can tail its source at all. */
  canFollow: boolean;
  /** The walk's own parameters — see followParams for what still stands between them and the session. */
  params?: Record<string, unknown>;
  loading: boolean;
  columnFilterKeys: Record<string, string>;
  filterConfig?: OperationResultFilterConfig;
  pagination?: DataTablePagination;
  download?: ClickyDownloadOptions;
  infinite?: DataTableInfinite;
}

type LogsTableProps = ComponentProps<typeof LogsTable>;

function FollowErrorNotice({ error }: { error: LogTailError }) {
  return (
    <p
      role="alert"
      className="flex items-center gap-1.5 text-xs text-amber-600 dark:text-amber-500"
    >
      <Icon icon={UiWarningTriangle} className="size-3.5 shrink-0" />
      {followErrorText(error)}
    </p>
  );
}

function FollowToggle({
  following,
  onFollowingChange,
  status,
  tailedRows,
}: {
  following: boolean;
  onFollowingChange: (following: boolean) => void;
  status: string;
  tailedRows: number;
}) {
  return (
    <div className="flex items-center gap-3">
      <Switch checked={following} onChange={onFollowingChange} label="Follow" />
      <span className="text-xs tabular-nums text-muted-foreground">
        {status}
        {tailedRows > 0 ? ` · ${tailedRows} tailed` : ""}
      </span>
    </div>
  );
}

function ScrollToLatestButton({ onClick }: { onClick: () => void }) {
  return (
    <Button
      type="button"
      size="sm"
      variant="secondary"
      aria-label="Scroll to latest"
      onClick={onClick}
      className="absolute bottom-4 left-1/2 -translate-x-1/2 gap-1.5 rounded-full shadow-md"
    >
      <Icon icon={UiArrowDown} className="size-3.5" />
      Latest
    </Button>
  );
}

/** The table props that exist only when the surface was given the matching input. */
function optionalTableProps({
  filterConfig,
  infinite,
  pagination,
  download,
}: Pick<
  LogsSurfaceProps,
  "filterConfig" | "infinite" | "pagination" | "download"
>): Partial<LogsTableProps> {
  return {
    ...(infinite ? { infinite } : {}),
    ...(filterConfig?.filters && filterConfig.filters.length > 0
      ? { externalFilters: filterConfig.filters }
      : {}),
    ...(filterConfig?.search ? { externalSearch: filterConfig.search } : {}),
    ...(filterConfig?.timeRange ? { externalTimeRange: filterConfig.timeRange } : {}),
    ...(pagination ? { pagination } : {}),
    ...(download ? { download } : {}),
  };
}

export function LogsSurface({
  history,
  profile,
  canFollow,
  params,
  loading,
  columnFilterKeys,
  filterConfig,
  pagination,
  download,
  infinite,
}: LogsSurfaceProps) {
  const [following, setFollowing] = useState(false);

  const tail = useLogTail({
    profile,
    params,
    following: canFollow && following,
    maxRows: TAIL_MAX_ROWS,
  });

  // The buffer survives switching Follow off, so what was tailed stays readable
  // and the seam above it stays drawn; only new rows stop arriving.
  const tailed = tail.rows.length > 0;
  const logs = tailed ? [...history, ...tail.rows] : history;
  const runs = followRunIndex(history, tail.rows);
  const { scrollRef, pinned, scrollToLatest } = useFollowScroll({
    following,
    rows: tail.rows,
  });

  return (
    <div className="flex h-full min-h-0 flex-col gap-2">
      {tail.error && <FollowErrorNotice error={tail.error} />}

      <div className="relative flex min-h-0 flex-1 flex-col">
        <LogsTable
          logs={logs}
          loading={loading}
          autoFilter={false}
          fullscreenTitle="Logs"
          defaultSort={LOGS_SORT}
          // The rows came back in the order the profile declared, and only some of
          // them are here. Re-sorting a partial walk in the browser would order the
          // pages fetched so far against each other and present the result as the
          // order of the whole.
          manualSort
          columnFilterKeys={columnFilterKeys}
          scrollContainerRef={scrollRef}
          getRowId={followRowId(runs)}
          // Fullscreen renders a second copy of the table inside a modal, and the
          // tail pins through a scroll handle only one of them can hold. DataTable
          // hands it to whichever copy is on screen, so following survives opening
          // and closing the modal and a log surface keeps its fullscreen control.
          showFullscreenControl
          cellFilters={filterConfig?.cellFilters}
          onCellFilterChange={filterConfig?.onCellFilterChange}
          {...(tailed
            ? {
                grouping: followGrouping(runs, {
                  droppedRows: tail.droppedRows,
                  startedAt: tail.session?.startedAt,
                }),
              }
            : {})}
          {...(canFollow
            ? {
                filterBarProps: {
                  trailing: (
                    <FollowToggle
                      following={following}
                      onFollowingChange={setFollowing}
                      status={tail.status}
                      tailedRows={tail.rows.length}
                    />
                  ),
                },
              }
            : {})}
          {...optionalTableProps({ filterConfig, infinite, pagination, download })}
        />

        {following && !pinned && logs.length > 0 && (
          <ScrollToLatestButton onClick={scrollToLatest} />
        )}
      </div>
    </div>
  );
}
