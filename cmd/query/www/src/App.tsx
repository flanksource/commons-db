import {
  EntityExplorerApp,
  ChatLayer,
  RouterProvider,
  ThemeProvider,
  createUnitFormExtensions,
  createOperationsApiClient,
  useBrowserRouter,
  useRouter,
  type ResultRenderContext,
} from "@flanksource/clicky-ui";
import { MonacoProvider } from "@flanksource/clicky-ui/monaco";
import {
  DebugConsoleButton,
  DebugConsoleDock,
  withDebugFetch,
} from "@flanksource/clicky-ui/devtools";
import { ChatButton, ChatWindowManagerProvider } from "@flanksource/clicky-ui/ai";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { secretFormExtensions } from "./secretKeySelector";
import { namespaceFormExtensions } from "./namespacePicker";
import { connectionFormActions } from "./connectionActions";
import { logsResultRenderer, useLogsSurfaces } from "./logsProfiles";
import { connectionDetailBodyRenderer, connectionDetailHeaderRenderer } from "./connectionBrowser";
import { connectionDashboardResultRenderer } from "./connectionDashboardRenderer";
import { getMonacoWorker } from "./monacoWorkers";
import { queryChatConfig } from "./chatWidget";
import { isQueryChatOperation } from "./chatOperations";
import {
  celEditorFormExtensions,
  esQueryBuilderFormExtensions,
  profileBuilderFormExtensions,
  profileEditSurfaceKey,
  processorPipelineFormExtensions,
} from "@flanksource/clicky-ui/profiles";
import { esParamOptionsFormExtensions } from "./esParamOptions";
import { jsonPathFormExtensions } from "./jsonPathPicker";
import { connectionLoggingFormExtensions } from "./connectionLogging";
import { BuildProfileButton } from "./buildProfileAction";
import { EditProfileButton, ProfileEditorPage } from "./editProfileAction";
import { isProfileSurface } from "./profileUpdateOperation";
import { profileRowDetailsResult } from "./profileRowDetails";
import { ReconcileButton } from "./reconcileAction";
import { ReconcilePage } from "./reconcilePage";
import { reconcileSurfaceKey } from "./reconcileModel";
import { ProfileInspectButton } from "./profileInspectAction";
import {
  configureProfileConnectionForm,
  useProfileConnectionMapping,
} from "./profileConnectionMapping";

const unitFormExtensions = createUnitFormExtensions();

// Compose the form extensions: the namespace picker, the secret/workload url
// selector (which reads the selected namespace from the form's root value), the
// profile query builder, and the structured OpenSearch filter builder that
// mounts on provider.options.search in both the create and the edit form.
// Exported so main.tsx can hand the same list to configureProfiles: the profile
// editor renders its own JsonSchemaForms inside clicky-ui, far from this app's
// EntityExplorerApp, and without them every x-clicky-component in the profile
// schema — the CEL editor, the processor pipeline, the namespace and secret
// pickers — falls back to a plain control.
export const formExtensions = {
  pre: [
    ...unitFormExtensions.pre,
    ...esParamOptionsFormExtensions.pre,
    ...connectionLoggingFormExtensions.pre,
  ],
  post: [
    ...namespaceFormExtensions.post,
    ...secretFormExtensions.post,
    ...profileBuilderFormExtensions.post,
    ...esQueryBuilderFormExtensions.post,
    ...esParamOptionsFormExtensions.post,
    ...jsonPathFormExtensions.post,
    ...celEditorFormExtensions.post,
    ...processorPipelineFormExtensions.post,
    ...connectionLoggingFormExtensions.post,
  ],
};

configureProfileConnectionForm({
  formPre: formExtensions.pre,
  formPost: formExtensions.post,
  footerActions: connectionFormActions,
});

// The Go server (query serve) exposes:
//   - the OpenAPI spec + executor under /api (entity discovery, list/get),
//   - mutations at POST/PUT/DELETE /api/v1/{connection,profile},
//   - profile execution at GET /api/v1/profile/{name}?<params>,
//   - and each resource's JSON Schema on the same endpoint via
//     `Accept: application/schema+json` (if/then connection schema, profile-setup
//     schema, and the per-profile FilterBar+columns schema).
//
// The EntityExplorerApp drives list/detail/filter UI from the OpenAPI spec. The
// schema-by-convention endpoints power the create/edit forms and the per-profile
// FilterBar; see cmd/query/README.md for the contract.
// Every request the explorer makes goes out armed at whatever the debug console
// is set to, and unarmed — with no header at all — when it is closed. The
// wrapper is applied here rather than to `window.fetch` so it captures this
// app's API traffic and not Vite's HMR socket or the chat stream.
const baseClient = createOperationsApiClient({
  baseUrl: "",
  openApiPath: "/api/openapi.json",
  fetch: withDebugFetch(),
});

// EntityExplorerApp consumes both @tanstack/react-query (data fetching) and
// clicky-ui's ThemeProvider (ThemeSwitcher) from context but provides neither
// itself, so the host app owns the QueryClient and theme lifecycle.
const queryClient = new QueryClient();

// Explorer reads the logs-surface set (needs the QueryClient context) and wires
// the result renderer so `render: logs` profiles present via clicky-ui LogsTable.
function Explorer() {
	const { client, dialog } = useProfileConnectionMapping(baseClient);
  const pathname = useRouter().pathname;
  const editingProfile = profileEditSurfaceKey(pathname);
  const reconcilingProfile = reconcileSurfaceKey(pathname);
  const logsSurfaces = useLogsSurfaces();
  const renderLogsResult = logsResultRenderer(logsSurfaces);
  const renderResult = (context: ResultRenderContext) => {
    const connectionResult = connectionDashboardResultRenderer(context);
    if (connectionResult !== context.defaultView) return connectionResult;
    const defaultResult = renderLogsResult(context);
    const result =
      defaultResult === context.defaultView && isProfileSurface(context.surfaceKey)
        ? profileRowDetailsResult(context, client, context.surfaceKey!)
        : defaultResult;
    const action =
      context.surfaceKey === "profiles" ? (
        <BuildProfileButton client={client} />
      ) : isProfileSurface(context.surfaceKey) ? (
        <>
          <EditProfileButton client={client} surfaceKey={context.surfaceKey!} />
          <ProfileInspectButton client={client} surfaceKey={context.surfaceKey!} />
          <ReconcileButton client={client} surfaceKey={context.surfaceKey!} />
        </>
      ) : null;
    if (!action) return result;
    return (
      <div className="flex h-full min-h-0 flex-col gap-4">
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          {action}
        </div>
        <div className="flex min-h-0 flex-1 flex-col">{result}</div>
      </div>
    );
  };
  return (
    <>
      {editingProfile ? (
        <ProfileEditorPage client={client} surfaceKey={editingProfile} />
      ) : reconcilingProfile ? (
        <ReconcilePage client={client} surfaceKey={reconcilingProfile} />
      ) : (
        <EntityExplorerApp
          client={client}
          actions={
            <>
              <DebugConsoleButton />
              <ChatButton label="Open query assistant" />
            </>
          }
          formExtensions={formExtensions}
          formActions={connectionFormActions}
          surfaceActionLabels={{
            connection: { create: "Add Connection", update: "Edit" },
            profiles: { create: "Add Profile" },
          }}
          resultRenderer={renderResult}
          entityDetailBodyRenderer={(context) =>
            connectionDetailBodyRenderer(
              context,
              ({ connectionName, providerType, providerOptions, profileQuery }) => (
                <BuildProfileButton
                  client={client}
                  connectionName={connectionName}
                  providerType={providerType}
                  {...(providerOptions ? { providerOptions } : {})}
                  {...(profileQuery ? { profileQuery } : {})}
                />
              ),
            )
          }
          entityDetailHeaderRenderer={connectionDetailHeaderRenderer}
        />
      )}
      <ChatLayer
        client={client}
        operationFilter={isQueryChatOperation}
        {...queryChatConfig}
      />
      {dialog}
    </>
  );
}

export function App() {
  const router = useBrowserRouter();
  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <MonacoProvider getWorker={getMonacoWorker}>
          <RouterProvider adapter={router}>
            <ChatWindowManagerProvider storageId="query-chat">
              {/* The dock is a flex sibling that shrinks the page rather than an
                  overlay, and it is mounted outside the router branch, so an
                  open console survives navigation onto the takeover pages
                  (profile editor, reconcile) that bypass the app shell. Its
                  trigger is navbar chrome and lives in EntityExplorerApp's
                  actions slot, so opening the console does start from the
                  explorer. */}
              <div
                className="flex min-h-0 flex-col overflow-hidden"
                style={{ height: "100dvh" }}
              >
                <div className="min-h-0 flex-1 overflow-hidden">
                  <Explorer />
                </div>
                <DebugConsoleDock />
              </div>
            </ChatWindowManagerProvider>
          </RouterProvider>
        </MonacoProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}
