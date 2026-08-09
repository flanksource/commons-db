import {
  createSecretFormExtensions,
  type KeyPreview,
  type OnePasswordField,
  type OnePasswordItem,
  type OnePasswordVault,
  type SecretFormLoaders,
  type SecretKind,
  type SecretResource,
  type SecretValueSource,
  type WorkloadKind,
  type WorkloadResource,
} from "@flanksource/clicky-ui";

const SECRET_SOURCES: SecretValueSource[] = [
  "secret",
  "configmap",
  "helm",
  "serviceaccount",
  "onepassword",
  "value",
];
const URL_SOURCES: SecretValueSource[] = [
  "secret",
  "configmap",
  "helm",
  "onepassword",
  "value",
];

async function fetchJSON<T>(url: string): Promise<T> {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`${url}: ${response.status} ${await response.text()}`);
  }
  return (await response.json()) as T;
}

function namespaceQuery(namespace: string): string {
  return namespace ? `&namespace=${encodeURIComponent(namespace)}` : "";
}

const loaders: SecretFormLoaders = {
  loadResources: (kind: SecretKind, namespace: string) =>
    fetchJSON<SecretResource[]>(
      `/api/v1/secrets?kind=${kind}${namespaceQuery(namespace)}`,
    ),
  loadKeyPreview: (kind: SecretKind, name: string, namespace: string) =>
    fetchJSON<KeyPreview[]>(
      `/api/v1/secrets/preview?kind=${kind}&name=${encodeURIComponent(name)}${namespaceQuery(namespace)}`,
    ),
  loadServiceAccounts: (namespace: string) =>
    fetchJSON<SecretResource[]>(
      `/api/v1/secrets?kind=serviceaccount${namespaceQuery(namespace)}`,
    ),
  loadOnePasswordVaults: () =>
    fetchJSON<OnePasswordVault[]>("/api/v1/secrets/onepassword/vaults"),
  loadOnePasswordItems: (vaultID: string) =>
    fetchJSON<OnePasswordItem[]>(
      `/api/v1/secrets/onepassword/items?vault=${encodeURIComponent(vaultID)}`,
    ),
  loadOnePasswordFields: (vaultID: string, itemID: string) =>
    fetchJSON<OnePasswordField[]>(
      `/api/v1/secrets/onepassword/fields?vault=${encodeURIComponent(vaultID)}&item=${encodeURIComponent(itemID)}`,
    ),
  loadWorkloads: (namespace: string, kinds: WorkloadKind[]) => {
    if (!namespace) {
      return Promise.resolve({
        service: [],
        ingress: [],
        deployment: [],
        statefulset: [],
      } satisfies Record<WorkloadKind, WorkloadResource[]>);
    }
    return fetchJSON<Record<WorkloadKind, WorkloadResource[]>>(
      `/api/v1/workloads?namespace=${encodeURIComponent(namespace)}&kinds=${kinds.join(",")}`,
    );
  },
};

export const secretFormExtensions = createSecretFormExtensions({
  loaders,
  secretSources: SECRET_SOURCES,
  urlSources: URL_SOURCES,
});
