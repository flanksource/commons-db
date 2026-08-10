import type { QueryBrowserResult } from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import {
  browserBaseUrl,
  fetchJSON,
  mergeProviderOptions,
  queryBrowserOptionsSchema,
  useInspection,
  type BrowserDescriptor,
} from "./connectionBrowserModel";
import {
  ConnectionQueryWorkspace,
  supportsQueryBuilder,
} from "./connectionQueryWorkspace";
import { defaultParamValues, paramRoles } from "./esQueryBuilder";
import type { EsSearch } from "./esQueryBuilderModel";
import {
  profileWizardErrorMessage,
  withProfileLimits,
  type ProfileColumn,
  type ProfileWizardDraft,
} from "./profileWizardModel";

type SampleResult = QueryBrowserResult & {
  columns: ProfileColumn[];
  renderedQuery: string;
};

/** What a sample yields: the discovered column shape plus the rows it came
 *  from, so the editor can preview the configured columns against real data. */
export type ProfileSample = {
  columns: ProfileColumn[];
  rows: Record<string, unknown>[];
  sourceDraft: ProfileWizardDraft;
};

type ProfileWizardQueryStepProps = {
  connectionID: string;
  draft: ProfileWizardDraft;
  discovered: ProfileColumn[];
  onDraftChange: (draft: ProfileWizardDraft) => void;
  onSample: (sample: ProfileSample) => void;
};

export function ProfileWizardQueryStep({
  connectionID,
  draft,
  discovered,
  onDraftChange,
  onSample,
}: ProfileWizardQueryStepProps) {
  const baseUrl = browserBaseUrl(connectionID);
  const descriptor = useQuery({
    queryKey: ["profile-wizard-descriptor", connectionID],
    queryFn: () => fetchJSON<BrowserDescriptor>(baseUrl),
    retry: 0,
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

  // The starter query is for a source whose artifact is the query. Where the
  // workspace builds filters instead, the specification is the artifact and a
  // query seeded beside it would be the pair the server rejects.
  const rawArtifact =
    descriptor.data !== undefined && !supportsQueryBuilder(descriptor.data);
  useEffect(() => {
    if (rawArtifact && !query && descriptor.data?.defaultQuery) {
      setQuery(descriptor.data.defaultQuery);
      onDraftChange({ ...draft, query: descriptor.data.defaultQuery });
    }
  }, [descriptor.data?.defaultQuery, draft, onDraftChange, query, rawArtifact]);

  const explicitTargetKind = liveOptions.targetKind ?? providerOptions.targetKind;
  const inspection = useInspection({
    cacheKey: "profile-wizard-inspection",
    id: connectionID,
    baseUrl,
    enabled: descriptor.data?.catalog === true,
    database: selectedDatabase,
    fallbackDatabase: String(providerOptions.database ?? ""),
    target: String(liveOptions.index ?? providerOptions.index ?? ""),
    ...(typeof explicitTargetKind === "string"
      ? { targetKind: explicitTargetKind }
      : {}),
  });
  const browserOptions = useMemo(
    () =>
      mergeProviderOptions({
        layers: [
          descriptor.data?.initialOptions,
          providerOptions,
          catalogOptions,
        ],
        database: inspection.sqlDatabase,
        keepTargetKind: true,
      }),
    [
      catalogOptions,
      descriptor.data?.initialOptions,
      inspection.sqlDatabase,
      providerOptions,
    ],
  );
  const effectiveOptions = useCallback(
    (options: Record<string, unknown>) =>
      mergeProviderOptions({
        layers: [providerOptions, catalogOptions, options],
        database: inspection.sqlDatabase,
      }),
    [catalogOptions, inspection.sqlDatabase, providerOptions],
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
      <ConnectionQueryWorkspace
        id={`profile-wizard:${connectionID}`}
        title="Profile query"
        descriptor={descriptor.data}
        inspection={inspection}
        onDatabaseChange={setSelectedDatabase}
        query={query}
        options={browserOptions}
        optionsSchema={queryBrowserOptionsSchema(descriptor.data)}
        search={providerOptions.search as EsSearch | undefined}
        onSearchChange={(transition) => {
          // Built from the merged options so a delete actually removes the key
          // rather than being reinstated by a lower layer on the next merge.
          const options = effectiveOptions(liveOptions);
          if (transition.search) options.search = transition.search;
          else delete options.search;
          setQuery(transition.query);
          onDraftChange({
            ...draft,
            query: transition.query,
            provider: { ...(draft.provider ?? {}), options },
          });
        }}
        params={draft.params ?? []}
        onParamMappingChange={(edit) => {
          const options = effectiveOptions(liveOptions);
          options.search = edit.search;
          setQuery("");
          onDraftChange({
            ...draft,
            query: "",
            params: edit.params,
            provider: { ...(draft.provider ?? {}), options },
          });
        }}
        paramValues={defaultParamValues(draft.params)}
        paramRoles={paramRoles(draft.params)}
        {...(draft.limits ? { limits: draft.limits } : {})}
        onLimitsChange={(limits) => onDraftChange(withProfileLimits(draft, limits))}
        compileBaseUrl={baseUrl}
        className="min-h-0 flex-1"
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
        onCatalogSelect={(node) => {
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
              options: effectiveOptions({ ...browserOptions, ...nextOptions }),
            },
          });
        }}
        execute={async (request) => {
          const nextDraft = {
            ...draft,
            query: request.query,
            provider: {
              ...(draft.provider ?? {}),
              options: effectiveOptions(request.options),
            },
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
              ...(request.pagination
                ? { pagination: request.pagination }
                : {}),
              ...(request.debug ? { debug: true } : {}),
            }),
          });
          onSample({
            columns: result.columns ?? [],
            rows: result.rows ?? [],
            sourceDraft: nextDraft,
          });
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
