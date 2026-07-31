import {
  parseSecretRef,
  serializeSecretRef,
  type EndpointMode,
  type EndpointSelectorValue,
  type EndpointTarget,
  type EndpointWorkloadValue,
} from "@flanksource/clicky-ui";

const SCHEME_TO_MODE = {
  svc: "service",
  ip: "cluster-ip",
  proxy: "api-proxy",
  host: "ingress",
  portforward: "port-forward",
} as const satisfies Record<string, EndpointMode>;

const MODE_TO_SCHEME = Object.fromEntries(
  Object.entries(SCHEME_TO_MODE).map(([scheme, mode]) => [mode, scheme]),
) as Record<Exclude<EndpointMode, "url">, keyof typeof SCHEME_TO_MODE>;

const WORKLOAD_URL =
  /^([a-z]+):\/\/([^/:?#]*)(?::(\d+))?([^?#]*)(?:\?(.+))?$/;

function parseTarget(
  mode: Exclude<EndpointMode, "url">,
  host: string,
  query: string,
): EndpointTarget | undefined {
  const dot = host.indexOf(".");
  const name = dot >= 0 ? host.slice(0, dot) : host;
  const namespace = dot >= 0 ? host.slice(dot + 1) : "";
  if (!name) return undefined;

  const kind =
    mode === "ingress"
      ? "ingress"
      : mode === "port-forward" &&
          new URLSearchParams(query).get("kind") === "deployment"
        ? "deployment"
        : "service";
  return {
    kind,
    name,
    ...(namespace ? { namespace } : {}),
  };
}

function parseWorkloadValue(raw: string): EndpointWorkloadValue | undefined {
  const match = WORKLOAD_URL.exec(raw);
  if (!match) return undefined;
  const [, scheme = "", host = "", port = "", path = "", query = ""] = match;
  const mode = SCHEME_TO_MODE[scheme as keyof typeof SCHEME_TO_MODE];
  if (!mode) return undefined;
  if (
    mode === "port-forward" &&
    (new URLSearchParams(query).has("selector") ||
      !["", "service", "deployment"].includes(
        new URLSearchParams(query).get("kind") ?? "",
      ))
  ) {
    return undefined;
  }

  const target = parseTarget(mode, host, query);
  if (!target) return undefined;
  return {
    mode,
    target,
    ...(port ? { port } : {}),
    ...(path ? { path } : {}),
  };
}

export function parseEndpointValue(
  raw: unknown,
): EndpointSelectorValue | undefined {
  if (typeof raw !== "string" || raw === "") return undefined;
  return (
    parseWorkloadValue(raw) ?? {
      mode: "url",
      source: parseSecretRef(raw)!,
    }
  );
}

function workloadHost(target: EndpointTarget) {
  if (!target.name) {
    throw new Error("Endpoint workload mode requires a workload name");
  }
  return target.namespace
    ? `${target.name}.${target.namespace}`
    : target.name;
}

function validateWorkloadValue(value: EndpointWorkloadValue) {
  if (value.mode === "ingress" && value.target.kind !== "ingress") {
    throw new Error("Ingress endpoint mode requires an ingress target");
  }
  if (
    value.mode !== "ingress" &&
    value.mode !== "port-forward" &&
    value.target.kind !== "service"
  ) {
    throw new Error(`${value.mode} endpoint mode requires a service target`);
  }
  if (
    value.mode === "port-forward" &&
    !["service", "deployment"].includes(value.target.kind)
  ) {
    throw new Error("Port-forward endpoint mode requires a service or deployment target");
  }
  if (
    value.port &&
    (!/^\d+$/.test(value.port) ||
      Number(value.port) < 1 ||
      Number(value.port) > 65535)
  ) {
    throw new Error(`Endpoint port "${value.port}" must be between 1 and 65535`);
  }
  if (value.path && !value.path.startsWith("/")) {
    throw new Error(`Endpoint path "${value.path}" must start with /`);
  }
}

export function serializeEndpointValue(
  value: EndpointSelectorValue | undefined,
): string {
  if (!value) return "";
  if (value.mode === "url") return serializeSecretRef(value.source);
  validateWorkloadValue(value);
  const scheme = MODE_TO_SCHEME[value.mode];
  const port = value.port ? `:${value.port}` : "";
  const path = value.path ?? "";
  const kind =
    value.mode === "port-forward" && value.target.kind === "deployment"
      ? "?kind=deployment"
      : "";
  return `${scheme}://${workloadHost(value.target)}${port}${path}${kind}`;
}
