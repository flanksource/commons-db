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

Run `query serve --dev --port 8088`, open `http://localhost:8088/connection`, and choose Add Connection. Enter a kubeconfig file or use Browse, select a context, and then select a namespace and secret. Save persists the selection; Resolve and Test use it before saving as well.
