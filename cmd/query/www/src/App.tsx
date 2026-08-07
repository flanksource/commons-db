import {
  EntityExplorerApp,
  RouterProvider,
  ThemeProvider,
  createOperationsApiClient,
  useBrowserRouter,
  useRouter,
  type ResultRenderContext,
} from "@flanksource/clicky-ui";
import { MonacoProvider } from "@flanksource/clicky-ui/monaco";
import { ChatWindowManagerProvider } from "@flanksource/clicky-ui/ai";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { secretFormExtensions } from "./secretKeySelector";
import { namespaceFormExtensions } from "./namespacePicker";
import { connectionFormActions } from "./connectionActions";
import { logsResultRenderer, useLogsEntityNames } from "./logsProfiles";
import { connectionDetailBodyRenderer, connectionDetailHeaderRenderer } from "./connectionBrowser";
import { getMonacoWorker } from "./monacoWorkers";
import { ChatWidget } from "./chatWidget";
import { profileBuilderFormExtensions } from "./profileBuilder";
import { esQueryBuilderFormExtensions } from "./esQueryBuilder";
import { esParamOptionsFormExtensions } from "./esParamOptions";
import { jsonPathFormExtensions } from "./jsonPathPicker";
import { BuildProfileButton } from "./buildProfileAction";
import {
  EditProfileButton,
  ProfileEditorPage,
  isProfileSurface,
} from "./editProfileAction";
import { profileEditSurfaceKey } from "./profileEditorModel";
import { profileRowDetailsResult } from "./profileRowDetails";
import { ReconcileButton } from "./reconcileAction";
import { ReconcilePage } from "./reconcilePage";
import { reconcileSurfaceKey } from "./reconcileModel";
import {
  configureProfileConnectionForm,
  useProfileConnectionMapping,
} from "./profileConnectionMapping";

// Compose the form extensions: the namespace picker, the secret/workload url
// selector (which reads the selected namespace from the form's root value), the
// profile query builder, and the structured OpenSearch filter builder that
// mounts on provider.options.search in both the create and the edit form.
const formExtensions = {
  pre: [...esParamOptionsFormExtensions.pre],
  post: [
    ...namespaceFormExtensions.post,
    ...secretFormExtensions.post,
    ...profileBuilderFormExtensions.post,
    ...esQueryBuilderFormExtensions.post,
    ...esParamOptionsFormExtensions.post,
    ...jsonPathFormExtensions.post,
  ],
};

configureProfileConnectionForm({
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
const baseClient = createOperationsApiClient({
  baseUrl: "",
  openApiPath: "/api/openapi.json",
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
  const logsEntityNames = useLogsEntityNames();
  const renderLogsResult = logsResultRenderer(logsEntityNames);
  const renderResult = (context: ResultRenderContext) => {
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
  // The editor is a route of its own rather than a dialog over the explorer:
  // six sections and a column grid need the whole viewport, a URL, and the back
  // button. It replaces the shell instead of nesting inside it.
  if (editingProfile) {
    return (
      <>
        <ProfileEditorPage client={client} surfaceKey={editingProfile} />
        {dialog}
      </>
    );
  }
  // Reconciling is a route for the same reason: two schemas, a key being worked
  // out against them, and a result read as triage all want the whole viewport.
  if (reconcilingProfile) {
    return (
      <>
        <ReconcilePage client={client} surfaceKey={reconcilingProfile} />
        {dialog}
      </>
    );
  }

  return (
    <>
      <EntityExplorerApp
        client={client}
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
          ({ connectionName, providerType, providerOptions }) => (
            <BuildProfileButton
              client={client}
              connectionName={connectionName}
              providerType={providerType}
              providerOptions={providerOptions}
            />
          ),
        )
      }
        entityDetailHeaderRenderer={connectionDetailHeaderRenderer}
      />
      <ChatWidget client={client} />
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
              <div className="min-h-0 overflow-hidden" style={{ height: "100dvh" }}>
                <Explorer />
              </div>
            </ChatWindowManagerProvider>
          </RouterProvider>
        </MonacoProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}
