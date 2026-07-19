import {
  QueryBrowser,
  type QueryBrowserCompletion,
  type QueryBrowserResult,
} from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  CatalogTree,
  completionForInspection,
  fetchJSON,
  queryBrowserOptionsSchema,
  type BrowserDescriptor,
  type BrowserInspection,
  type CatalogNode,
} from "./connectionBrowser";
import {
  profileWizardErrorMessage,
  type ProfileColumn,
  type ProfileWizardDraft,
} from "./profileWizardModel";

type SampleResult = QueryBrowserResult & {
  columns: ProfileColumn[];
  renderedQuery: string;
};

type ProfileWizardQueryStepProps = {
  connectionID: string;
  draft: ProfileWizardDraft;
  discovered: ProfileColumn[];
  onDraftChange: (draft: ProfileWizardDraft) => void;
  onSample: (columns: ProfileColumn[]) => void;
};

export function ProfileWizardQueryStep({
  connectionID,
  draft,
  discovered,
  onDraftChange,
  onSample,
}: ProfileWizardQueryStepProps) {
  const baseUrl = `/api/v1/connection/${encodeURIComponent(connectionID)}/browser`;
  const descriptor = useQuery({
    queryKey: ["profile-wizard-descriptor", connectionID],
    queryFn: () => fetchJSON<BrowserDescriptor>(baseUrl),
    retry: 0,
  });
  const baseInspection = useQuery({
    queryKey: ["profile-wizard-inspection", connectionID],
    queryFn: () => fetchJSON<BrowserInspection>(`${baseUrl}/inspect`),
    enabled: descriptor.data?.catalog === true,
    retry: 0,
    staleTime: 5 * 60_000,
  });
  const providerOptions = useMemo(
    () => ({ ...(draft.provider?.options ?? {}) }),
    [draft.provider?.options],
  );
  const [query, setQuery] = useState(draft.query ?? "");
  const [liveOptions, setLiveOptions] = useState(providerOptions);
  const [catalogOptions, setCatalogOptions] = useState<Record<string, unknown>>(
    {},
  );
  const [selectedDatabase, setSelectedDatabase] = useState("");

  useEffect(() => {
    if (!query && descriptor.data?.defaultQuery) {
      setQuery(descriptor.data.defaultQuery);
      onDraftChange({ ...draft, query: descriptor.data.defaultQuery });
    }
  }, [descriptor.data?.defaultQuery, draft, onDraftChange, query]);

  const databaseInspection = useQuery({
    queryKey: ["profile-wizard-inspection", connectionID, selectedDatabase],
    queryFn: () => {
      const params = new URLSearchParams({ database: selectedDatabase });
      return fetchJSON<BrowserInspection>(`${baseUrl}/inspect?${params}`);
    },
    enabled:
      baseInspection.data?.kind === "sql" &&
      selectedDatabase !== "" &&
      selectedDatabase !== baseInspection.data.database,
    retry: 0,
    staleTime: 5 * 60_000,
  });
  const inspection =
    selectedDatabase && selectedDatabase !== baseInspection.data?.database
      ? databaseInspection
      : baseInspection;
  const inspectionData = inspection.data ?? baseInspection.data;
  const activeDatabase =
    selectedDatabase ||
    String(providerOptions.database ?? "") ||
    inspectionData?.database ||
    "";
  const browserOptions = useMemo<Record<string, unknown>>(
    () => ({
      ...(descriptor.data?.initialOptions ?? {}),
      ...providerOptions,
      ...catalogOptions,
      ...(activeDatabase && inspectionData?.kind === "sql"
        ? { database: activeDatabase }
        : {}),
    }),
    [
      activeDatabase,
      catalogOptions,
      descriptor.data?.initialOptions,
      inspectionData?.kind,
      providerOptions,
    ],
  );
  const selectedTargetName = String(
    liveOptions.index ?? browserOptions.index ?? "",
  );
  const selectedTargetKind = useMemo(() => {
    const explicit = liveOptions.targetKind ?? browserOptions.targetKind;
    if (typeof explicit === "string") return explicit;
    return (
      inspectionData?.targets?.find(
        (target) => target.name === selectedTargetName,
      )?.kind ?? ""
    );
  }, [
    browserOptions.targetKind,
    inspectionData?.targets,
    liveOptions.targetKind,
    selectedTargetName,
  ]);
  const selectedInspection = useQuery({
    queryKey: [
      "profile-wizard-inspection",
      connectionID,
      selectedTargetKind,
      selectedTargetName,
    ],
    queryFn: () => {
      const params = new URLSearchParams({
        target: selectedTargetName,
        targetKind: selectedTargetKind,
      });
      return fetchJSON<BrowserInspection>(`${baseUrl}/inspect?${params}`);
    },
    enabled:
      inspectionData?.kind === "opensearch" &&
      selectedTargetName !== "" &&
      selectedTargetKind !== "",
    retry: 0,
    staleTime: 5 * 60_000,
  });
  const completion = useMemo<QueryBrowserCompletion | undefined>(
    () => completionForInspection(inspectionData, selectedInspection.data),
    [inspectionData, selectedInspection.data],
  );
  const effectiveOptions = useCallback(
    (options: Record<string, unknown>) => {
      const next: Record<string, unknown> = {
        ...providerOptions,
        ...catalogOptions,
        ...options,
        ...(activeDatabase && inspectionData?.kind === "sql"
          ? { database: activeDatabase }
          : {}),
      };
      delete next.targetKind;
      return next;
    }, [activeDatabase, catalogOptions, inspectionData?.kind, providerOptions],
  );

  if (descriptor.isLoading) {
    return <QueryStepMessage>Loading connection browser…</QueryStepMessage>;
  }
  if (descriptor.isError) {
    return (
      <QueryStepMessage error>
        {profileWizardErrorMessage(
          descriptor.error,
          "Unable to load this connection browser",
        )}
      </QueryStepMessage>
    );
  }
  if (!descriptor.data) {
    return (
      <QueryStepMessage>
        This saved connection does not expose a query browser.
      </QueryStepMessage>
    );
  }

  return (
    <div className="flex min-h-[32rem] flex-1 flex-col gap-3">
      <div className="flex items-center justify-between gap-4 rounded-lg border bg-muted/30 px-4 py-3">
        <div>
          <p className="text-sm font-medium">Explore the source, then run a sample</p>
          <p className="text-xs text-muted-foreground">
            Sampling discovers fields without saving the profile.
          </p>
        </div>
        <span className="shrink-0 rounded-full bg-background px-3 py-1 text-xs font-medium text-muted-foreground">
          {discovered.length
            ? `${discovered.length} fields discovered`
            : "No sample yet"}
        </span>
      </div>
      <QueryBrowser
        id={`profile-wizard:${connectionID}`}
        title="Profile query"
        language={descriptor.data.language ?? "text"}
        queryLabel={descriptor.data.queryLabel ?? "Query"}
        initialQuery={query}
        optionsSchema={queryBrowserOptionsSchema(descriptor.data)}
        initialOptions={browserOptions}
        completion={completion}
        onQueryChange={(nextQuery) => {
          setQuery(nextQuery);
          onDraftChange({ ...draft, query: nextQuery });
        }}
        onOptionsChange={(options) => {
          setLiveOptions(options);
          onDraftChange({
            ...draft,
            provider: {
              ...(draft.provider ?? {}),
              options: effectiveOptions(options),
            },
          });
        }}
        className="min-h-0 flex-1"
        navigator={
          descriptor.data.catalog ? (
            <CatalogTree
              nodes={inspectionData?.nodes ?? []}
              loading={inspection.isLoading}
              error={inspection.error}
              databases={baseInspection.data?.databases ?? []}
              database={activeDatabase}
              onDatabaseChange={setSelectedDatabase}
              onSelect={(node: CatalogNode) => {
                const nextQuery = node.query ?? query;
                const nextOptions = node.options ?? {};
                setQuery(nextQuery);
                setCatalogOptions(nextOptions);
                setLiveOptions({ ...browserOptions, ...nextOptions });
                onDraftChange({
                  ...draft,
                  query: nextQuery,
                  provider: {
                    ...(draft.provider ?? {}),
                    options: effectiveOptions({
                      ...browserOptions,
                      ...nextOptions,
                    }),
                  },
                });
              }}
            />
          ) : undefined
        }
        execute={async (request) => {
          const options = effectiveOptions(request.options);
          const nextDraft = {
            ...draft,
            query: request.query,
            provider: { ...(draft.provider ?? {}), options },
          };
          onDraftChange(nextDraft);
          const result = await fetchJSON<SampleResult>("/api/v1/profile/sample", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              profile: {
                ...nextDraft,
                profile: nextDraft.profile || "sample",
              },
              params: {},
            }),
          });
          onSample(result.columns ?? []);
          return result;
        }}
      />
    </div>
  );
}

function QueryStepMessage({
  children,
  error = false,
}: {
  children: ReactNode;
  error?: boolean;
}) {
  return (
    <div
      className={`grid min-h-96 flex-1 place-items-center rounded-xl border border-dashed p-8 text-sm ${error ? "border-destructive/40 text-destructive" : "text-muted-foreground"}`}
    >
      {children}
    </div>
  );
}
