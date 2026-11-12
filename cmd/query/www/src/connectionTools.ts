import { useRouter } from "@flanksource/clicky-ui";

export type BrowserTab = "browser" | "trace" | "traces" | "logs";

export function connectionToolSelection(provider: string | undefined, requested: string | null) {
  let tabs: readonly BrowserTab[] = ["browser"];
  let defaultTab: BrowserTab = "browser";
  switch (provider) {
    case "k8s":
      tabs = ["logs", "browser"];
      defaultTab = "logs";
      break;
    case "opentelemetry":
      defaultTab = "traces";
      tabs = ["browser", "traces"];
      break;
    case "opensearch":
      tabs = ["browser", "traces"];
      break;
    case "sqlserver":
      tabs = ["browser", "trace"];
      break;
  }
  const tab = requested || defaultTab;
  const activeTab = tabs.find(candidate => candidate === tab) ?? "browser";
  return { tabs, activeTab };
}

export function useConnectionTools(provider: string | undefined) {
  const { navigate, pathname } = useRouter();
  const selection = connectionToolSelection(provider, new URLSearchParams(window.location.search).get("tab"));
  const selectTab = (tab: BrowserTab) => {
    const params = new URLSearchParams(window.location.search);
    params.set("tab", tab);
    navigate(`${pathname}?${params}`, { replace: true });
  };
  return { ...selection, selectTab };
}
