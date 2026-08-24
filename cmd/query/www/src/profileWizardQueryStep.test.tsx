import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { BrowserDescriptor } from "./connectionBrowserModel";
import type { EsCompileRequest } from "./esQueryPreview";
import {
  ProfileWizardQueryStep,
  profileSamplePayload,
} from "./profileWizardQueryStep";
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
  const html = renderToStaticMarkup(
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
  return { html, inputs: compileInputs };
};

// The editor's Source section previews through this step, and the preview is
// compiled server-side: without the declared parameter values a {{.params.…}}
// operand compiles to the compiler's refusal to guess rather than to the DSL a
// run produces.
describe("ProfileWizardQueryStep compilation", () => {
  it("compiles the specification against the declared parameter defaults", () => {
    const { inputs } = renderStep([
      { name: "service", type: "enum", default: "payments" },
      { name: "since", type: "string", default: "now-1h", role: "time-from" },
    ]);
    expect(inputs.length).toBeGreaterThan(0);
    expect(inputs[0]?.params).toEqual({ service: "payments", since: "now-1h" });
    expect(inputs[0]?.roles).toEqual({ since: "time-from" });
  });

  it("sends no parameter values when the profile declares none", () => {
    const { inputs } = renderStep(undefined);
    expect(inputs.length).toBeGreaterThan(0);
    expect(inputs[0]?.params).toEqual({});
  });
});

describe("ProfileWizardQueryStep layout", () => {
  it("fills the modal viewport and leaves scrolling to the browser panes", () => {
    const { html } = renderStep(undefined);

    expect(html).toContain(
      'class="flex h-full min-h-0 flex-1 flex-col gap-3 overflow-hidden"',
    );
    expect(html).toContain('class="h-full min-h-0 flex-1"');
    expect(html).not.toContain("h-[calc(100vh-15rem)]");
  });
});

describe("profile sample payload", () => {
  it("keeps UI-only profile state out of the strict sample request", () => {
    expect(
      profileSamplePayload(
        {
          ...draft(undefined),
          _id: "profile-record-1",
          query: "{\"query\":{\"match_all\":{}}}",
          columns: [{ name: "message", type: "string" }],
        },
        {
          query: "{\"query\":{\"match_all\":{}}}",
          options: { index: "logs" },
          pagination: { limit: 25 },
          debug: true,
        },
      ),
    ).toEqual({
      profile: {
        profile: "kenya",
        provider: {
          type: "opensearch",
          connection: `connection://${connectionID}`,
          options: {
            index: "logs",
            search: {
              query: {
                op: "term",
                field: "service.name",
                value: "{{.params.service}}",
              },
            },
          },
        },
        query: "{\"query\":{\"match_all\":{}}}",
      },
      params: {},
      pagination: { limit: 25 },
      debug: true,
    });
  });
});
