import { describe, expect, it } from "vitest";
import { connectionProfileTargetOptions } from "./connectionProfileTarget";

describe("connection profile target options", () => {
  it("carries a selected Kubernetes workload into Build Profile", () => {
    expect(
      connectionProfileTargetOptions(
        {
          kind: "query",
          provider: "k8s",
          target: {
            kind: "kubernetes-workload",
            label: "Workload",
            kinds: ["pod", "deployment", "statefulset", "daemonset"],
          },
        },
        {
          kind: "DaemonSet",
          namespace: "observability",
          name: "node-agent",
          limit: "200",
        },
      ),
    ).toEqual({
      kind: "DaemonSet",
      namespace: "observability",
      name: "node-agent",
    });
  });

  it("does not emit a partial Kubernetes target", () => {
    expect(
      connectionProfileTargetOptions(
        {
          kind: "query",
          provider: "k8s",
          target: {
            kind: "kubernetes-workload",
            label: "Workload",
            kinds: ["pod"],
          },
        },
        { kind: "Pod", namespace: "payments" },
      ),
    ).toBeUndefined();
  });

  it("keeps the existing flat index target behavior", () => {
    expect(
      connectionProfileTargetOptions(
        {
          kind: "query",
          provider: "opensearch",
          target: { kind: "index", label: "Index" },
        },
        { index: "logs-*", targetKind: "pattern" },
      ),
    ).toEqual({ index: "logs-*" });
  });
});
