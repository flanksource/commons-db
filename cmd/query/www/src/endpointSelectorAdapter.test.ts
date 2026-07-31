import { describe, expect, it } from "vitest";
import type { EndpointSelectorValue } from "@flanksource/clicky-ui";
import {
  parseEndpointValue,
  serializeEndpointValue,
} from "./endpointSelectorAdapter";

describe("endpoint selector persistence adapter", () => {
  it.each([
    [
      "svc://db.prod:5432/api",
      {
        mode: "service",
        target: { kind: "service", name: "db", namespace: "prod" },
        port: "5432",
        path: "/api",
      },
    ],
    [
      "ip://db.prod:5432",
      {
        mode: "cluster-ip",
        target: { kind: "service", name: "db", namespace: "prod" },
        port: "5432",
      },
    ],
    [
      "proxy://app.prod:8080/PASJava",
      {
        mode: "api-proxy",
        target: { kind: "service", name: "app", namespace: "prod" },
        port: "8080",
        path: "/PASJava",
      },
    ],
    [
      "host://search.prod:8443/api",
      {
        mode: "ingress",
        target: { kind: "ingress", name: "search", namespace: "prod" },
        port: "8443",
        path: "/api",
      },
    ],
    [
      "portforward://search.prod:9200?kind=deployment",
      {
        mode: "port-forward",
        target: { kind: "deployment", name: "search", namespace: "prod" },
        port: "9200",
      },
    ],
  ] satisfies [string, EndpointSelectorValue][])(
    "round-trips %s",
    (stored, selectorValue) => {
      expect(parseEndpointValue(stored)).toEqual(selectorValue);
      expect(serializeEndpointValue(selectorValue)).toBe(stored);
    },
  );

  it("round-trips URL literals and references through SecretKeyValue", () => {
    expect(parseEndpointValue("https://search.example.com")).toEqual({
      mode: "url",
      source: { kind: "value", value: "https://search.example.com" },
    });
    expect(
      serializeEndpointValue({
        mode: "url",
        source: { kind: "secret", name: "search", key: "url" },
      }),
    ).toBe("secret://search/url");
  });

  it("preserves selector-based port-forward values as URL literals", () => {
    const stored = "portforward://.prod:9200?selector=app%3Dsearch";
    expect(parseEndpointValue(stored)).toEqual({
      mode: "url",
      source: { kind: "value", value: stored },
    });
  });

  it("rejects incomplete workload values instead of serializing an invalid URL", () => {
    expect(() =>
      serializeEndpointValue({
        mode: "service",
        target: { kind: "service", name: "" },
      }),
    ).toThrow("requires a workload name");
  });
});
