import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import {
  ProfileEditorRaw,
  parseProfileYamlDocument,
  profileYamlFilename,
} from "./profileEditorRaw";

vi.mock("@flanksource/clicky-ui/monaco", () => ({
  MonacoSchemaEditor: ({ height, value }: { height?: string | number; value: string }) => (
    <div data-slot="monaco-editor" data-height={height}>
      {value}
    </div>
  ),
}));

const draft = {
  profile: "service-logs",
  provider: { type: "opensearch" },
  query: "GET service-logs/_search",
};

describe("raw profile YAML", () => {
  it("fills the editor frame and exposes YAML import and export", () => {
    const html = renderToStaticMarkup(
      <ProfileEditorRaw
        draft={draft}
        onChange={() => undefined}
        onValidityChange={() => undefined}
      />,
    );

    expect(html).toContain("Import YAML");
    expect(html).toContain("Export YAML");
    expect(html).toContain('accept=".yaml,.yml,application/yaml,text/yaml,text/x-yaml"');
    expect(html).toContain('data-slot="profile-yaml-editor-frame"');
    expect(html).toContain("[&amp;&gt;[data-slot=monaco-editor]]:h-full");
    expect(html).toContain('data-height="100%"');
  });

  it("parses an imported YAML object", () => {
    expect(
      parseProfileYamlDocument(`
profile: service-logs
provider:
  type: opensearch
query: GET service-logs/_search
`),
    ).toEqual(draft);
  });

  it("rejects an imported YAML document that is not an object", () => {
    expect(() => parseProfileYamlDocument("- service-logs\n")).toThrow(
      "Profile YAML must contain an object",
    );
  });

  it("uses a filesystem-safe YAML export name", () => {
    expect(profileYamlFilename(" Service Logs / Prod ")).toBe("Service-Logs-Prod.yaml");
    expect(profileYamlFilename(" ")).toBe("profile.yaml");
  });
});
