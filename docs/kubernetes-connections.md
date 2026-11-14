# Kubernetes selection for connections

Connections can select a Kubernetes cluster for secret, ConfigMap, Helm, service-account, and service URL references. Kubernetes connections also use the selection for cluster queries, logs, discovery, and connection tests.

```yaml
name: orders
type: postgres
namespace: shop
url: postgres://$(username):$(password)@db/orders
username: configmap://db/user
password: secret://db/password
properties:
  kubeconfig: ~/.kube/target.yaml
  kubecontext: staging-admin
```

`properties.kubeconfig` is an optional file path on the server running the application. Absolute paths, paths relative to the server working directory, and `~/` paths are supported. An empty value uses standard client-go kubeconfig discovery, including merged `$KUBECONFIG` files.

`properties.kubecontext` is an optional context name in that configuration. An empty value uses the configuration's current context. Leaving both fields empty preserves the application's ambient Kubernetes client. An explicit missing file or context fails instead of selecting a different cluster.

The connection editor offers a context picker and accepts typed values. Kubeconfig file browsing is available only over localhost; remote users can type a server-side path. Browsing lists file metadata and never uploads or returns file contents.

The kubeconfig file and context controls share a row. Logging shows the current levels and slow threshold in a compact summary. Use Edit logging to adjust them, then Apply to update the connection draft or Cancel to discard the popup's changes. Save the connection to persist the draft.

Kubernetes resolution is grouped separately from endpoint authentication. Editing the file path preserves the selected context; a context absent from the new file is reported explicitly. Connection forms validate required fields and incompatible kubeconfig sources before saving. Closing an edited connection asks whether to discard the draft or keep editing.

For Kubernetes connections, use either `properties.kubeconfig` or the existing `certificate` kubeconfig content/reference field. Setting both is rejected. No operation changes the source kubeconfig's current context.

Hosts using the connection service expose `GET /api/v1/connections/contexts?kubeconfig=...`, returning `{ "currentContext": "...", "contexts": ["..."] }`. Namespace, secret, preview, and workload lookup endpoints accept optional `kubeconfig` and `kubecontext` query parameters. The shared `clicky-ui` connection form supplies these from the current draft.

## Workload URLs

A connection URL can name a workload instead of an address: `<strategy>://<name>[.<namespace>][:<port>][/path][?kind=&selector=&port=]`. Hydration expands it with the selected cluster; the application scheme (`postgres`, `http`, ...) comes from the connection type.

| Strategy | Expands to | Backends |
|---|---|---|
| `svc://` | `<name>.<namespace>.svc.cluster.local` | any |
| `ip://` | the Service ClusterIP | any |
| `host://` | the first Ingress host (`https` when the Ingress terminates TLS for it) | HTTP |
| `proxy://` | an apiserver `services/proxy` or `pods/proxy` URL | HTTP |
| `portforward://` | `localhost:<port>` through an SPDY port-forward | any |

`proxy://` and `portforward://` take `kind`: `service` (default), `pod`, `deployment`, `statefulset`, `daemonset` or `replicaset`. For `proxy://`:

- `kind=service` builds `/api/v1/namespaces/<ns>/services/<scheme>:<name>:<port>/proxy/<path>`; the apiserver resolves the Service and a named Service port.
- `kind=pod` builds `/api/v1/namespaces/<ns>/pods/<scheme>:<name>:<port>/proxy/<path>`. The pod must be Ready.
- A workload kind resolves to one Ready pod matched by the workload's `spec.selector`, the first by name, then proxies to that pod. Without a name, `selector=` selects the pods directly: `proxy://.shop:8080/?kind=deployment&selector=app%3Dorders`.
- A named port goes in `?port=` (a URL authority carries only numbers): `proxy://orders.shop/health?kind=statefulset&port=http`. It resolves against the container ports, or the Service ports for `kind=service`.
- No Ready pod, an unknown kind, or an unknown port name fails resolution, naming the workload and namespace.

Query parameters other than `kind`, `selector` and `port` stay on the expanded URL.

### Authentication through the apiserver proxy

Requests to an expanded `proxy://` URL authenticate to the apiserver with the kubeconfig identity that resolved it: bearer token, basic, exec plugin or client certificate, plus its CA. `HTTPConnection.Transport()` and `CreateHTTPClient` apply this automatically.

The apiserver deletes the `Authorization` header after it authenticates a request, before it proxies it to the Service or pod. A backend behind `proxy://` therefore never receives a basic, bearer or OAuth credential sent in `Authorization`. A connection with such credentials, or with TLS settings (which would apply to the apiserver hop), fails with an error instead of silently dropping them. To authenticate to the backend:

- pass the credential as a query parameter, if the backend accepts one, for example `proxy://orders.shop:8080/api?kind=pod&token=...`;
- have the backend read a different header than `Authorization`; or
- use `svc://`, `host://` or `portforward://`, which keep the `Authorization` header end to end.

### RBAC

| Strategy | Permissions in the target namespace |
|---|---|
| `ip://` | `get` services |
| `host://` | `get` ingresses (networking.k8s.io) |
| `proxy://` with `kind=service` | `get`, `create` on `services/proxy` |
| `proxy://` with `kind=pod` | `get` pods; `get`, `create` on `pods/proxy` |
| `proxy://` with a workload kind | `get` on the kind (apps), `list` pods; `get`, `create` on `pods/proxy` |
| `portforward://` | `get`/`list` pods (and services or the workload kind); `create` on `pods/portforward` |

The apiserver checks a proxied request against the verb of its HTTP method: `GET` needs `get`, `POST` needs `create`, and `PUT`, `PATCH` and `DELETE` need `update`, `patch` and `delete` on `services/proxy` or `pods/proxy`.

## Trying it out

Run `query serve --dev --port 8088`, open `http://localhost:8088/connection`, and choose Add Connection. Enter a kubeconfig file or use Browse, select a context, and then select a namespace and secret. Save persists the selection; Resolve and Test use it before saving as well.
