import {
  EntityExplorerApp,
  RouterProvider,
  ThemeProvider,
  createOperationsApiClient,
  useBrowserRouter,
} from "@flanksource/clicky-ui";
import { MonacoProvider } from "@flanksource/clicky-ui/monaco";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { secretFormExtensions } from "./secretKeySelector";
import { namespaceFormExtensions } from "./namespacePicker";
import { connectionFormActions } from "./connectionActions";
import { logsResultRenderer, useLogsEntityNames } from "./logsProfiles";
import { connectionDetailBodyRenderer } from "./connectionBrowser";
import { getMonacoWorker } from "./monacoWorkers";

// Compose the form extensions: the namespace picker, plus the secret/workload
// url selector (which reads the selected namespace from the form's root value).
const formExtensions = {
  post: [...namespaceFormExtensions.post, ...secretFormExtensions.post],
};

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
const client = createOperationsApiClient({
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
  const logsEntityNames = useLogsEntityNames();
  return (
    <EntityExplorerApp
      client={client}
      formExtensions={formExtensions}
      formActions={connectionFormActions}
      surfaceActionLabels={{
        connection: { create: "Add Connection", update: "Edit" },
        profiles: { create: "Add Profile" },
      }}
      resultRenderer={logsResultRenderer(logsEntityNames)}
      entityDetailBodyRenderer={connectionDetailBodyRenderer}
    />
  );
}

export function App() {
  const router = useBrowserRouter();
  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <MonacoProvider getWorker={getMonacoWorker}>
          <RouterProvider adapter={router}>
            <Explorer />
          </RouterProvider>
        </MonacoProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}
