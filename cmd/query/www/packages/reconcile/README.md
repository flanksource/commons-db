# @flanksource/commons-db-ui

Shared React reconciliation screen used by commons-db's query app. Hosts supply
their profile catalog, authenticated `OperationsApiClient`, and navigation;
the package owns the bench, saved configuration, snapshot results, and exports.
It does not start a server or install a router.

This is a source package: the consuming bundler must compile TypeScript/TSX.
React 18/19, React Query 5, and clicky-ui are peer dependencies so the host and
screen share the same contexts. Mount under the host's `QueryClientProvider`
and clicky-ui theme provider. Import clicky-ui's stylesheet and, in the host's
Tailwind 4 stylesheet, include:

```css
@import "@flanksource/commons-db-ui/source.css";
```

Use the same semantic theme tokens as clicky-ui. The CSS entry registers this
package's source with the host's Tailwind build; it does not duplicate preflight.

```tsx
<ReconcilePage
  key={sourceName}
  client={authenticatedClient}
  sourceName={sourceName}
  loadProfiles={loadProfileDocuments}
  profilesQueryKey={["profiles"]}
  navigation={{
    search: location.search,
    view: snapshotId ? "results" : "bench",
    snapshotId,
    backHref: "/traces",
    benchHref: `/reconcile/${encodeURIComponent(sourceName)}`,
    snapshotHref: (id) => `/reconcile/${encodeURIComponent(sourceName)}/${encodeURIComponent(id)}`,
    virtualProfileHref: (snapshot) => `/profiles/${encodeURIComponent(snapshot.profile)}`,
    navigate,
  }}
/>
```

The host must update `navigation` when its location changes. Bench and snapshot
hrefs may include host query parameters (such as `tab` and `source`); the screen
replaces its filters and result-view parameters while preserving those host
parameters. `loadProfiles` returns profile documents, not executed rows.

Mount this screen under a `QueryClient` whose whole lifecycle is scoped to the
active server and authentication context. Discard that client before changing
server or identity. Changing `profilesQueryKey` or remounting `ReconcilePage`
alone is insufficient: clicky-ui's OpenAPI cache and this package's snapshot
descriptor and result caches also live in the enclosing client. Keep the
profile query key stable within one such context. `onProfileSaved` can
invalidate additional host caches; the supplied profile query key is always
invalidated by the screen itself.

All operation paths are discovered from the client's OpenAPI metadata. The
backend must register commons-db's profile actions (`reconcile`,
`reconcile-materialize`, `reconcile-snapshot`, update, and profile execution).
Its profile service requires snapshot storage, a store overlay that exposes
snapshot profiles, and snapshot connection/lease resolvers in its execution
context. See `cmd/query/internal/app/app.go` and `server.go` for the existing
`snapshots.Manager` wiring, including startup and shutdown. Registering actions
without configuring snapshots is insufficient.

The client and profile loader carry the host's authentication. Export downloads
use URLs returned by the backend; those URLs must be accessible using the host's
browser session. Neither the package nor its URLs should contain credentials.

For a local consumer before publication, run `pnpm pack` in this directory and
install the resulting tarball in the consuming app. Publishing the package and
updating consumers to a registry version are separate release steps.
