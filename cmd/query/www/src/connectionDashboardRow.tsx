import { Icon, cn, useRouter } from "@flanksource/clicky-ui";
import { UiKey, UiLink, UiLock, UiWarningTriangle } from "@flanksource/clicky-ui/icons";
import { ConnectionTypeIcon } from "./iconProvider";
import type {
  ConnectionDashboardHealthState,
  ConnectionDashboardItem,
  ConnectionHealth,
} from "./connectionDashboardModel";

export const HEALTH_LABELS: Record<ConnectionDashboardHealthState, string> = {
  healthy: "Reachable",
  credentials: "Credentials failed",
  unreachable: "Unreachable",
  unverifiable: "Not verifiable",
  unknown: "Check did not complete",
};

const HEALTH_LEGEND_LABELS: Record<ConnectionDashboardHealthState, string> = {
  healthy: "reachable",
  credentials: "credentials failed",
  unreachable: "unreachable",
  unverifiable: "no endpoint to probe",
  unknown: "check did not complete",
};

const HEALTH_STYLES: Record<
  ConnectionDashboardHealthState,
  { dot: string; edge: string }
> = {
  healthy: { dot: "bg-emerald-400", edge: "border-l-emerald-400" },
  credentials: { dot: "bg-amber-400", edge: "border-l-amber-400" },
  unreachable: { dot: "bg-rose-400", edge: "border-l-rose-400" },
  unverifiable: { dot: "bg-muted-foreground", edge: "border-l-border" },
  unknown: { dot: "bg-muted-foreground/50", edge: "border-l-border" },
};

const UNCHECKED_EDGE = "border-l-transparent";

export function ConnectionDashboardRow({
  connection,
  health,
  pending,
  onCheck,
}: {
  connection: ConnectionDashboardItem;
  health?: ConnectionHealth;
  pending: boolean;
  onCheck: (ids: string[], options?: { force?: boolean }) => void;
}) {
  const { renderLink } = useRouter();
  return (
    <div
      className={cn(
        "flex min-w-0 items-center gap-3 border-l-2 py-2 pl-3 pr-2 hover:bg-muted/30",
        health ? HEALTH_STYLES[health.state].edge : UNCHECKED_EDGE,
      )}
    >
      {/* The dot sits outside the link: a button nested in an anchor is invalid
          markup and the anchor swallows its click. */}
      <HealthDot
        health={health}
        pending={pending}
        onCheck={() => onCheck([connection.id], { force: Boolean(health) })}
      />
      {renderLink({
        to: `/connection/${encodeURIComponent(connection.id)}`,
        title: health?.detail,
        className:
          "flex min-w-0 flex-1 items-center gap-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset",
        children: (
          <>
            <span className="shrink-0" aria-hidden>
              <ConnectionTypeIcon type={connection.type} className="size-4" />
            </span>
            <span className="w-32 shrink-0 truncate text-sm font-medium md:w-40" title={connection.name}>
              {connection.name}
            </span>
            <div className="min-w-0 flex-1">
              <EndpointLine endpoint={connection.endpoint} />
            </div>
            <div className="hidden shrink-0 items-center gap-1.5 lg:flex">
              <SecretChip count={connection.secretCount} />
              <RiskChips connection={connection} />
            </div>
            <span className="w-24 shrink-0 text-right text-xs text-muted-foreground">
              {connection.profileCount === 0 ? (
                <span className="text-muted-foreground/60">unused</span>
              ) : (
                `${connection.profileCount} profile${connection.profileCount === 1 ? "" : "s"}`
              )}
            </span>
            <span className="hidden w-16 shrink-0 text-right text-xs text-muted-foreground/70 xl:inline">
              {ageLabel(connection.updatedAt)}
            </span>
          </>
        ),
      })}
    </div>
  );
}

function HealthDot({
  health,
  pending,
  onCheck,
}: {
  health?: ConnectionHealth;
  pending: boolean;
  onCheck: () => void;
}) {
  const label = pending
    ? "Checking health"
    : health
      ? `${HEALTH_LABELS[health.state]} — check again`
      : "Check health";
  return (
    <button
      type="button"
      onClick={onCheck}
      disabled={pending}
      title={health?.detail ?? label}
      aria-label={label}
      className="shrink-0 rounded-full p-1 hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <span
        className={cn(
          "block size-2 rounded-full",
          pending
            ? "animate-pulse bg-muted-foreground/60"
            : health
              ? HEALTH_STYLES[health.state].dot
              : "border border-muted-foreground/50",
        )}
      />
    </button>
  );
}

function EndpointLine({ endpoint }: { endpoint: ConnectionDashboardItem["endpoint"] }) {
  if (!endpoint) {
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
        <Icon icon={UiLink} className="text-xs" />
        <span className="italic">Resolved from ambient config</span>
      </span>
    );
  }
  const indirect = ["connection", "host", "portforward"].includes(endpoint.scheme);
  return (
    <span className="flex min-w-0 items-center gap-1.5 font-mono text-xs">
      <span
        className={cn(
          "shrink-0 rounded px-1 py-0.5 text-[10px] ring-1 ring-inset",
          indirect
            ? "bg-violet-100 text-violet-700 ring-violet-300/60 dark:bg-violet-400/10 dark:text-violet-300 dark:ring-violet-400/25"
            : "bg-muted text-muted-foreground ring-border",
        )}
      >
        {endpoint.scheme}
      </span>
      <span className="truncate text-foreground" title={`${endpoint.host}${endpoint.path ?? ""}`}>
        {endpoint.host}
        {endpoint.path ? <span className="text-muted-foreground">{endpoint.path}</span> : null}
      </span>
    </span>
  );
}

function SecretChip({ count }: { count: number }) {
  if (count === 0) return null;
  return (
    <span className="inline-flex items-center gap-1 rounded bg-teal-100 px-1.5 py-0.5 text-[11px] text-teal-700 ring-1 ring-inset ring-teal-300/60 dark:bg-teal-400/10 dark:text-teal-300 dark:ring-teal-400/25">
      <Icon icon={UiLock} className="text-[11px]" />
      {count} secret{count === 1 ? "" : "s"}
    </span>
  );
}

function RiskChips({ connection }: { connection: ConnectionDashboardItem }) {
  return (
    <>
      {connection.inlineCredential ? (
        <span className="inline-flex items-center gap-1 rounded bg-rose-100 px-1.5 py-0.5 text-[11px] text-rose-700 ring-1 ring-inset ring-rose-300/60 dark:bg-rose-400/10 dark:text-rose-300 dark:ring-rose-400/25">
          <Icon icon={UiKey} className="text-[11px]" />
          Password in URL
        </span>
      ) : null}
      {connection.insecureTLS ? (
        <span className="inline-flex items-center gap-1 rounded bg-amber-100 px-1.5 py-0.5 text-[11px] text-amber-800 ring-1 ring-inset ring-amber-300/60 dark:bg-amber-400/10 dark:text-amber-300 dark:ring-amber-400/25">
          <Icon icon={UiWarningTriangle} className="text-[11px]" />
          TLS verification off
        </span>
      ) : null}
    </>
  );
}

export function ConnectionHealthLegend() {
  return (
    <p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
      <span className="inline-flex items-center gap-1.5">
        <span className="inline-block size-2 rounded-full border border-muted-foreground/50" />
        not checked
      </span>
      {(["healthy", "credentials", "unreachable", "unverifiable", "unknown"] as const).map(
        (state) => (
          <span key={state} className="inline-flex items-center gap-1.5">
            <span className={cn("inline-block h-3 w-0.5", HEALTH_STYLES[state].dot)} />
            {HEALTH_LEGEND_LABELS[state]}
          </span>
        ),
      )}
    </p>
  );
}

function ageLabel(updatedAt: string): string {
  const timestamp = Date.parse(updatedAt);
  if (!Number.isFinite(timestamp)) return "—";
  return `${Math.max(0, Math.floor((Date.now() - timestamp) / 86_400_000))}d`;
}
