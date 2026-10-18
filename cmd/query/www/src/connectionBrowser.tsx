import {
  CacheBrowser,
  type EntityDetailBodyRenderContext,
  type EntityDetailHeaderRenderContext,
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
import { connectionProfileTargetOptions } from "./connectionProfileTarget";

export function connectionDetailBodyRenderer(
  context: EntityDetailBodyRenderContext,
  renderProfileAction?: ConnectionProfileActionRenderer,
): ReactNode {
  if (context.surfaceKey !== "connection") return context.defaultView;
  const connectionName =
    typeof context.entity?.name === "string" ? context.entity.name : context.id;
  return (
    <ConnectionBrowser
      key={context.id}
      id={context.id}
      connectionName={connectionName}
      fallback={context.defaultView}
      renderProfileAction={renderProfileAction}
    />
  );
}

type BrowserTab = "browser" | "trace";

function ConnectionBrowser({
  id,
  connectionName,
  fallback,
  renderProfileAction,
}: {
  id: string;
  connectionName: string;
  fallback: ReactNode;
  renderProfileAction?: ConnectionProfileActionRenderer;
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
      descriptor={descriptor.data}
      renderProfileAction={renderProfileAction}
    />
  );
}

function ConnectionBrowserBody({
  id,
  baseUrl,
  connectionName,
  descriptor,
  renderProfileAction,
}: {
  id: string;
  baseUrl: string;
  connectionName: string;
  descriptor: BrowserDescriptor;
  renderProfileAction?: ConnectionProfileActionRenderer;
}) {
  // The target the browser is pointed at, so "Build profile" starts where the
  // author left off. It is reported by the browser rather than picked here —
  // the picker lives in the browser's navigator.
  const [selectedOptions, setSelectedOptions] = useState<
    Record<string, unknown>
  >({});
  const [selectedQuery, setSelectedQuery] = useState<string>();
  const [activeTab, setActiveTab] = useState<BrowserTab>("browser");
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
      {hasTraceTab ? (
        <ConnectionToolTabs activeTab={activeTab} onSelect={setActiveTab} />
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
  onSelect,
}: {
  activeTab: BrowserTab;
  onSelect: (tab: BrowserTab) => void;
}) {
  return (
    <div
      className="flex gap-1 border-b"
      role="tablist"
      aria-label="Connection tools"
    >
      {(["browser", "trace"] as const).map((tab) => (
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
