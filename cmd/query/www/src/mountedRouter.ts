import { useMemo } from "react";
import { useBrowserRouter, type RouterAdapter } from "@flanksource/clicky-ui";
import { mountedRoute, mountPath, unmountedPath } from "./mount";

/**
 * The browser router with the mount taken off the pathname the app routes on
 * and put back on every link and navigation it makes.
 */
export function useMountedBrowserRouter(mount: string = mountPath): RouterAdapter {
  const router = useBrowserRouter();
  return useMemo<RouterAdapter>(() => {
    if (!mount) return router;
    return {
      ...router,
      pathname: unmountedPath(router.pathname, mount),
      navigate: (to, opts) => router.navigate(mountedRoute(to, mount), opts),
      renderLink: (props) => router.renderLink({ ...props, to: mountedRoute(props.to, mount) }),
    };
  }, [router, mount]);
}
