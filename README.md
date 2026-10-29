# commons-db

Shared Go database, connection, query, migration, and observability packages. The [query CLI and web app](cmd/query/README.md) consume these packages; the [documentation site](docs/README.md) covers embedding and storage APIs.

## Reusable connections

Applications use `github.com/flanksource/commons-db/context` to resolve connection references into `models.Connection`, then hydrate credentials and URL templates. The shared `connection` package supplies provider-specific clients. This works in standalone tools without a Mission Control server or database: attach a file store or an application-owned resolver to the context.

### Standalone file store and XDG lookup

`connection/store.Open("my-app")` reads the following layers, from lowest to highest priority:

| Layer | With `XDG_CONFIG_HOME` set | When unset |
| --- | --- | --- |
| `global` | `$XDG_CONFIG_HOME/flanksource/connections/` | `~/.config/flanksource/connections/` |
| `app` | `$XDG_CONFIG_HOME/my-app/connections/` | `~/.config/my-app/connections/` |
| `cwd` | `./.connections/` | `./.connections/` |

The app name is supplied by the caller; it must be non-empty and different from `flanksource`. The working-directory layer uses the process's current directory when the store is opened. The `~/.config` default applies on every operating system, including macOS; this helper does not use `os.UserConfigDir()` or search `XDG_CONFIG_DIRS`.

A later layer **replaces the whole connection** with the same namespace and name. For example, `.connections/orders.yaml` overrides an app or global definition of `shop/orders`; fields are not merged across files. Connections in different namespaces remain distinct.

Each directory is scanned directly for non-hidden `.yaml`, `.yml`, and `.json` files; subdirectories are not scanned. Missing layer directories are empty layers. Files are read on each lookup, so edits take effect on the next lookup. Within one layer, duplicate namespace/name definitions produce a warning on the first file in filename order; hydration refuses a connection carrying warnings. Invalid files remain visible in listings with their source path and warnings, and fail when used.

For custom directories or ordering, use `store.New(store.Layer{Name: "shared", Dir: sharedDir}, store.Layer{Name: "project", Dir: projectDir})`. Later arguments have higher priority. `Entries()` and `Find(reference)` expose the effective connection's layer and file path for diagnostics.

### Connection file syntax

For example, save this as `.connections/orders.yaml` in the directory where the application runs:

```yaml
apiVersion: mission-control.flanksource.com/v1
kind: Connection
metadata:
  name: orders
  namespace: shop
spec:
  postgres:
    host:
      value: localhost:5432
    database:
      value: orders
    username:
      value: app
    password:
      valueFrom:
        onePassword: op://development/orders/password
```

The file store uses the shared `connection/crd.Connection` envelope and typed `spec` variants, such as `postgres`, `mssql`, `http`, or `aws`. The `apiVersion` identifies the file format; loading it locally does not contact Mission Control or require installing a Kubernetes CRD. The example's 1Password reference requires an authenticated `op` CLI at hydration time. Literal `{value: ...}` credentials require no external secret service.

Use exactly one typed variant. If `metadata.name` is omitted, the filename without its extension supplies the name. `metadata.namespace` is optional. Flat files with top-level `name`, `type`, and `url` are rejected by this store. The older generic fields under `spec` (`type`, `url`, `username`, etc.) remain accepted but are deprecated; new files should use a typed variant. `store.Save(layer, resource)` writes this envelope with owner-only file permissions (`0600`).

### Reference syntax and resolution order

| Reference | File-store behavior |
| --- | --- |
| `connection://shop/orders` | Match the name `orders` in namespace `shop`. A file with an empty namespace also matches. |
| `connection://orders` | Match the name across namespaces; fail if more than one effective connection matches. |
| `orders` | Bare-name shorthand when a resolver is attached. Prefer `connection://orders` for an explicit reference. |
| `<connection-uuid>` | Match an effective file connection's generated ID. Use names for portable configuration: file IDs depend on layer name, namespace, and connection name. |

The deprecated `connection://<type>/<namespace>/<name>` form is also parsed, ignoring its type segment. A namespace-free file acts as a match for any requested namespace; if it and an explicitly namespaced file both match, the result is an ambiguity error rather than an automatic preference.

After `connections.Attach(ctx)`, explicit connection references resolve in this order:

1. The effective file connection, with `cwd` overriding `app` overriding `global` for the same namespace/name.
2. The resolver already attached to the context, if the file store returns no match.
3. The context's database `connections` table, if neither resolver owns the reference.
4. Credential hydration and URL templating for the selected connection.

Errors stop resolution; an invalid or ambiguous local definition does not fall through to an older resolver or database connection. A file-store hit needs no database. An explicit reference unresolved by the resolvers requires a configured database, otherwise resolution returns `db is not configured`.

In the database path, UUIDs select by ID, `connection://<name>` selects by name without inheriting the caller's namespace, and `connection://<namespace>/<name>` selects by namespace/name before the deprecated name/type compatibility lookup. These rules differ from older Duty/Mission Control versions that scoped an unqualified name to the context namespace.

Literal URLs such as `postgres://localhost/orders` are handled directly. A bare name is first offered to the resolver as `connection://<name>`; if unclaimed, it is parsed as an inline URL rather than looked up in the database. Use the explicit prefix when an unknown name should fail as a missing connection.

### Embed the file store

This complete example resolves and hydrates `orders` from the layers for `my-app`, without starting a database or making a database connection:

```go
package main

import (
	"fmt"
	"log"

	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
)

func main() {
	connections, err := store.Open("my-app")
	if err != nil {
		log.Fatal(err)
	}
	ctx := connections.Attach(dbcontext.New())
	connection, err := ctx.HydrateConnectionByURL("connection://shop/orders")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Resolved %s (%s)\n", connection.Name, connection.Type)
}
```

Pass the hydrated model to the relevant provider client. To expose file-backed connection CRUD through the query API/UI, use `cmd/query/connections.NewStoreProvider` with its `Store` and explicit `WriteLayer`; connections in other layers remain read-only.

### Application-owned connections without the resource format

Applications that already own configuration can attach a `context.ConnectionResolver` directly and return a `models.Connection`. This generic seam does not require the Mission Control resource envelope or the file store:

```go
func resolveConnection(_ dbcontext.Context, reference string) (*models.Connection, error) {
	if reference != "connection://orders" {
		return nil, nil
	}
	return &models.Connection{
		Name:     "orders",
		Type:     models.ConnectionTypePostgres,
		URL:      "postgres://$(username):$(password)@localhost:5432/orders",
		Username: "app",
		Password: "op://development/orders/password",
	}, nil
}
```

Import `github.com/flanksource/commons-db/models` alongside the context package, attach with `ctx := dbcontext.New().WithConnectionResolver(resolveConnection)`, and call `ctx.HydrateConnectionByURL("connection://orders")`. Return `(nil, nil)` only for references the resolver does not own; return an error for owned configuration that is invalid. Hydration updates the returned model in place, so return a fresh model and properties map rather than shared mutable catalog data. `WithConnectionResolver` replaces the current resolver; `Store.Attach` preserves and chains to it.

### Credential lookup

Connection URL, username, password, certificate, and property values support these string references. Typed file fields express them using `valueFrom`, as in the example above.

| Reference | Source |
| --- | --- |
| `op://<vault>/<item>/<field>` | 1Password through the `op` CLI; no Kubernetes client required. |
| `secret://<name>/<key>` | Kubernetes Secret key. |
| `configmap://<name>/<key>` | Kubernetes ConfigMap key. |
| `helm://<release>/<key>` | JSONPath in merged deployed Helm release values. |
| `serviceaccount://<name>` | Kubernetes ServiceAccount token. |

Kubernetes lookups use the connection's namespace, or the context namespace if empty, and its `properties.kubecontext` cluster when set; otherwise they use the context's local Kubernetes client. Each URI selects its source explicitly. Lookup errors propagate instead of trying another secret source.

Hydration resolves these values and workload URLs, then renders templates such as `$(username)`, `$(password)`, and `$(properties.host)`; username and password substitutions are URL-escaped. Hydrated connections are not cached as a whole; narrower credential and port-forward caches provide reuse.
