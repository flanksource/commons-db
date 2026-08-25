import { describe, expect, it } from "vitest";
import { connectionProfileTargetOptions } from "./connectionProfileTarget";

describe("connection profile target options", () => {
  it("never carries Kubernetes targets as provider options", () => {
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
    ).toBeUndefined();
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

  it("keeps the target under the provider-owned option", () => {
    expect(
      connectionProfileTargetOptions(
        {
          kind: "query",
          provider: "opensearch",
          target: { kind: "index", label: "Index", option: "index" },
        },
        { index: "logs-*", targetKind: "pattern" },
      ),
    ).toEqual({ index: "logs-*" });
  });

  it("supports providers whose target is not named index", () => {
    expect(
      connectionProfileTargetOptions(
        {
          kind: "query",
          provider: "azureloganalytics",
          target: {
            kind: "index",
            label: "Workspace",
            option: "workspaceID",
          },
        },
        { workspaceID: "workspace-2", index: "unrelated" },
      ),
    ).toEqual({ workspaceID: "workspace-2" });
  });
});
