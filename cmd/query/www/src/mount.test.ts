import { describe, expect, it } from "vitest";
import { mountedRoute, mountPathOf, unmountedPath } from "./mount";

describe("the app mounted under a host's prefix", () => {
  it("reads the mount from the <base> the server served", () => {
    expect(mountPathOf("http://gavel.local/query/")).toBe("/query");
    expect(mountPathOf("http://query.local/")).toBe("");
  });

  it("puts the mount on the app's routes and takes it off the location", () => {
    const route = "/profile/traces%2Fterminal?stream=s1&afterSeq=2";
    expect(mountedRoute(route, "/query")).toBe(`/query${route}`);
    expect(mountedRoute(`/query${route}`, "/query")).toBe(`/query${route}`);
    expect(mountedRoute("https://example.com/x", "/query")).toBe("https://example.com/x");
    expect(mountedRoute(route, "")).toBe(route);

    expect(unmountedPath("/query/profile/x", "/query")).toBe("/profile/x");
    expect(unmountedPath("/query", "/query")).toBe("/");
    expect(unmountedPath("/queryx/a", "/query")).toBe("/queryx/a");
  });
});
