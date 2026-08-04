import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { BrowserDescriptor } from "./connectionBrowserModel";
import type { EsCompileRequest } from "./esQueryPreview";
import { ProfileWizardQueryStep } from "./profileWizardQueryStep";
import type { ProfileWizardDraft } from "./profileWizardModel";

// The compile request only leaves the browser once effects run, which server
// rendering never does — so the wiring is asserted on what the hook was handed.
const compileInputs = vi.hoisted(() => [] as EsCompileRequest[]);
vi.mock("./esQueryPreview", async (importOriginal) => {
  const original = await importOriginal<typeof import("./esQueryPreview")>();
  return {
    ...original,
    useCompiledSearch: (input: EsCompileRequest) => {
      compileInputs.push(input);
      return original.useCompiledSearch(input);
    },
  };
});

const connectionID = "os-stub";

const descriptor: BrowserDescriptor = {
  kind: "query",
  provider: "opensearch",
  language: "json",
  catalog: true,
  targetLabel: "Index",
  optionsSchema: {
    type: "object",
    properties: {
      search: {
        type: "object",
        "x-clicky-component": "es-query-builder",
        "x-es-operators": [
          { op: "term", label: "term", arity: "single", fieldTypes: ["keyword"] },
        ],
      },
    },
  },
};

const draft = (params: ProfileWizardDraft["params"]): ProfileWizardDraft => ({
  profile: "kenya",
  provider: {
    type: "opensearch",
    connection: `connection://${connectionID}`,
    options: {
      index: "logs",
      search: {
        query: { op: "term", field: "service.name", value: "{{.params.service}}" },
      },
    },
  },
  ...(params ? { params } : {}),
});

const renderStep = (params: ProfileWizardDraft["params"]) => {
  compileInputs.length = 0;
  const client = new QueryClient();
  // The step renders nothing until the browser descriptor resolves, and server
  // rendering never fetches — so it is seeded rather than awaited.
  client.setQueryData(["profile-wizard-descriptor", connectionID], descriptor);
  renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <ProfileWizardQueryStep
        connectionID={connectionID}
        draft={draft(params)}
        discovered={[]}
        onDraftChange={() => {}}
        onSample={() => {}}
      />
    </QueryClientProvider>,
  );
  return compileInputs;
};

// The editor's Source section previews through this step, and the preview is
// compiled server-side: without the declared parameter values a {{.params.…}}
// operand compiles to the compiler's refusal to guess rather than to the DSL a
// run produces.
describe("ProfileWizardQueryStep compilation", () => {
  it("compiles the specification against the declared parameter defaults", () => {
    const inputs = renderStep([
      { name: "service", type: "enum", default: "payments" },
      { name: "since", type: "string", default: "now-1h", role: "time-from" },
    ]);
    expect(inputs.length).toBeGreaterThan(0);
    expect(inputs[0]?.params).toEqual({ service: "payments", since: "now-1h" });
    expect(inputs[0]?.roles).toEqual({ since: "time-from" });
  });

  it("sends no parameter values when the profile declares none", () => {
    const inputs = renderStep(undefined);
    expect(inputs.length).toBeGreaterThan(0);
    expect(inputs[0]?.params).toEqual({});
  });
});
