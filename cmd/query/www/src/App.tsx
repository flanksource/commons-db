import {
  EntityExplorerApp,
  RouterProvider,
  createOperationsApiClient,
  useBrowserRouter,
} from "@flanksource/clicky-ui";

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

export function App() {
  const router = useBrowserRouter();
  return (
    <RouterProvider adapter={router}>
      <EntityExplorerApp client={client} />
    </RouterProvider>
  );
}
