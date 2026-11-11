import {
  CacheBrowser,
  EntitySurface,
  useRouter,
  type EntityDetailBodyRenderContext,
  type EntityDetailHeaderRenderContext,
  type OperationsApiClient,
  type ResultRenderer,
} from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";
import {
  browserBaseUrl,
  type BrowserDescriptor,
  type ConnectionProfileActionRenderer,
} from "@flanksource/clicky-ui/profiles";
import { ConnectionInfoHeader } from "./connectionInfoHeader";
import { ConnectionQueryBrowser } from "./connectionQueryBrowser";
import { ConnectionTrace } from "./connectionTrace";
import { ConnectionOtel } from "./connectionOtel";
import { connectionProfileTargetOptions } from "./connectionProfileTarget";
import { connectionLogsSurface } from "./logsProfiles";
import { useProfiles } from "./profilesQuery";

/** What a connection's detail page lists its generated logs profile with. */
export type ConnectionLogsView = {
  client: OperationsApiClient;
  resultRenderer: ResultRenderer;
};

export function connectionDetailBodyRenderer(
  context: EntityDetailBodyRenderContext,
  options: { renderProfileAction?: ConnectionProfileActionRenderer; logs: ConnectionLogsView },
): ReactNode {
  if (context.surfaceKey !== "connection") return context.defaultView;
  const connectionName =
    typeof context.entity?.name === "string" ? context.entity.name : context.id;
  const namespace = typeof context.entity?.namespace === "string" ? context.entity.namespace : undefined;
  return (
    <ConnectionBrowser
      key={context.id}
      id={context.id}
      connectionName={connectionName}
      namespace={namespace}
      fallback={context.defaultView}
      renderProfileAction={options.renderProfileAction}
      logs={options.logs}
    />
  );
}

type BrowserTab = "browser" | "trace" | "traces" | "logs";

function ConnectionBrowser({
  id,
  connectionName,
  namespace,
  fallback,
  renderProfileAction,
  logs,
}: {
  id: string;
  connectionName: string;
  namespace: string | undefined;
  fallback: ReactNode;
  renderProfileAction: ConnectionProfileActionRenderer | undefined;
  logs: ConnectionLogsView;
}) {
  const baseUrl = browserBaseUrl(id);
  const descriptor = useQuery({
    queryKey: ["connection-browser", id],
    queryFn: async () => {
      const response = await fetch(baseUrl);
      if (response.status === 404) return null;
      if (!response.ok)
        throw new Error(
          (await response.text()).trim() ||
            `request failed: ${response.status}`,
        );
      return response.json() as Promise<BrowserDescriptor>;
    },
    retry: 0,
  });

  if (descriptor.isLoading) {
    return (
      <div className="rounded-xl border bg-card p-6 text-sm text-muted-foreground">
        Loading connection browser…
      </div>
    );
  }
  if (descriptor.isError) {
    return (
      <div className="rounded-xl border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
        {descriptor.error instanceof Error
          ? descriptor.error.message
          : "Failed to load connection browser"}
      </div>
    );
  }
  if (!descriptor.data) return fallback;
  if (descriptor.data.kind === "cache") {
    return (
      <div className="flex min-h-[32rem] flex-col gap-3">
        <div className="h-[calc(100vh-15rem)] min-h-[32rem] overflow-hidden rounded-xl border bg-card">
          <CacheBrowser baseUrl={baseUrl} />
        </div>
      </div>
    );
  }
  return (
    <ConnectionBrowserBody
      id={id}
      baseUrl={baseUrl}
      connectionName={connectionName}
      namespace={namespace}
      descriptor={descriptor.data}
      renderProfileAction={renderProfileAction}
      logs={logs}
    />
  );
}

function ConnectionBrowserBody({
  id,
  baseUrl,
  connectionName,
  namespace,
  descriptor,
  renderProfileAction,
  logs,
}: {
  id: string;
  baseUrl: string;
  connectionName: string;
  namespace: string | undefined;
  descriptor: BrowserDescriptor;
  renderProfileAction: ConnectionProfileActionRenderer | undefined;
  logs: ConnectionLogsView;
}) {
  // The target the browser is pointed at, so "Build profile" starts where the
  // author left off. It is reported by the browser rather than picked here —
  // the picker lives in the browser's navigator.
  const [selectedOptions, setSelectedOptions] = useState<
    Record<string, unknown>
  >({});
  const [selectedQuery, setSelectedQuery] = useState<string>();
  const { navigate, pathname } = useRouter();
  const supportsOtel = descriptor.provider === "opensearch" || descriptor.provider === "opentelemetry";
  // A Kubernetes connection opens on its logs: the browser is for authoring a
  // target, the logs are what the connection is usually opened to read.
  const supportsLogs = descriptor.provider === "k8s";
  const tab = new URLSearchParams(window.location.search).get("tab");
  const activeTab: BrowserTab = supportsLogs && (tab === "logs" || !tab)
    ? "logs"
    : supportsOtel && (tab === "traces" || (!tab && descriptor.provider === "opentelemetry")) ? "traces" : descriptor.provider === "sqlserver" && tab === "trace" ? "trace" : "browser";
  const profileOptions = useMemo(
    () => connectionProfileTargetOptions(descriptor, selectedOptions),
    [descriptor, selectedOptions],
  );
  const hasTraceTab = descriptor.provider === "sqlserver";
  const profileQuery = selectedQuery ?? descriptor.defaultQuery;
  const profileAction =
    descriptor.provider && renderProfileAction
      ? renderProfileAction({
          connectionName,
          providerType: descriptor.provider,
          ...(profileOptions ? { providerOptions: profileOptions } : {}),
          ...(profileQuery ? { profileQuery } : {}),
        })
      : null;
  return (
    <div className="flex min-w-0 flex-col gap-3">
      {profileAction ? (
        <div className="flex flex-wrap items-center gap-2">{profileAction}</div>
      ) : null}
      {hasTraceTab || supportsOtel || supportsLogs ? (
        <ConnectionToolTabs
          activeTab={activeTab}
          tabs={supportsLogs ? ["logs", "browser"] : supportsOtel ? ["browser", "traces"] : ["browser", "trace"]}
          onSelect={tab => { const params = new URLSearchParams(window.location.search); params.set("tab", tab); navigate(`${pathname}?${params}`, { replace: true }); }}
        />
      ) : null}
      {supportsLogs && activeTab === "logs" ? (
        <ConnectionLogs connection={{ name: connectionName, namespace }} {...logs} />
      ) : null}
      <div hidden={activeTab !== "browser"}>
        <ConnectionQueryBrowser
          id={id}
          baseUrl={baseUrl}
          descriptor={descriptor}
          onOptionsChange={setSelectedOptions}
          onQueryChange={setSelectedQuery}
        />
      </div>
      {supportsOtel && activeTab === "traces" && <ConnectionOtel id={id} name={connectionName} />}
      {hasTraceTab ? (
        <div hidden={activeTab !== "trace"}>
          <ConnectionTrace
            id={id}
            defaultDatabase={
              typeof descriptor.initialOptions?.database === "string"
                ? descriptor.initialOptions.database
                : undefined
            }
          />
        </div>
      ) : null}
    </div>
  );
}

function ConnectionToolTabs({
  activeTab,
  tabs,
  onSelect,
}: {
  activeTab: BrowserTab;
  tabs: readonly BrowserTab[];
  onSelect: (tab: BrowserTab) => void;
}) {
  return (
    <div
      className="flex gap-1 border-b"
      role="tablist"
      aria-label="Connection tools"
    >
      {tabs.map((tab) => (
        <button
          key={tab}
          role="tab"
          aria-selected={activeTab === tab}
          className={`border-b-2 px-3 py-2 text-sm font-medium capitalize ${activeTab === tab ? "border-primary text-foreground" : "border-transparent text-muted-foreground"}`}
          onClick={() => onSelect(tab)}
        >
          {tab}
        </button>
      ))}
    </div>
  );
}

function ConnectionLogs({
  connection,
  client,
  resultRenderer,
}: ConnectionLogsView & { connection: { name: string; namespace: string | undefined } }) {
  const profiles = useProfiles();
  if (profiles.isPending) {
    return <div className="text-sm text-muted-foreground">Loading logs…</div>;
  }
  if (profiles.isError) {
    return <div role="alert" className="text-sm text-destructive">{profiles.error.message}</div>;
  }
  const surfaceKey = connectionLogsSurface(profiles.data, connection);
  if (!surfaceKey) {
    return (
      <div role="alert" className="text-sm text-destructive">
        {`No logs profile is generated for connection ${connection.namespace ? `${connection.namespace}/` : ""}${connection.name}.`}
      </div>
    );
  }
  return <EntitySurface client={client} surfaceKey={surfaceKey} resultRenderer={resultRenderer} />;
}

export function connectionDetailHeaderRenderer(
  context: EntityDetailHeaderRenderContext,
): ReactNode {
  if (context.surfaceKey !== "connection") return context.defaultHeader;
  return (
    <ConnectionInfoHeader
      id={context.id}
      icon={context.icon}
      fallbackName={context.title}
    />
  );
}
