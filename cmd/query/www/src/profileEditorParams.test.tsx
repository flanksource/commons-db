import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import {
  configureProfiles,
  ProfileSchemaSection,
  type ProfileSchema,
  type ProfileWizardDraft,
} from "@flanksource/clicky-ui/profiles";
import profileSchemaDocument from "../../../../schemas/profile.json";

// The library takes its schema from the host at boot, so this test has to do
// what main.tsx does before it can render anything from it.
configureProfiles({
  schema: profileSchemaDocument as unknown as ProfileSchema,
});

// Renders the real Parameters section against the committed schemas/profile.json,
// so a schema regenerated without the accordion hints — or a clicky-ui that stops
// honouring them — fails here rather than in someone's browser. Interaction
// (expand, add, reorder) is covered by clicky-ui's own accordion tests; what
// matters here is that OUR generated schema drives it.
const DRAFT = {
  profile: "Kubernetes Logs",
  params: [
    {
      name: "namespace",
      label: "Namespace",
      type: "string",
      role: "filter",
      required: true,
      field: "resource.k8s.namespace",
    },
    { name: "pod", label: "Pod prefix", type: "list" },
  ],
} as unknown as ProfileWizardDraft;

function markup(draft: ProfileWizardDraft = DRAFT): string {
  return renderToStaticMarkup(
    <ProfileSchemaSection
      draft={draft}
      keys={["params"]}
      title="Parameters"
      description="Named values the profile query and filters accept at run time."
      idPrefix="profile-parameters"
      layout={{ mode: "stacked", valueMaxWidth: "none", help: "hover" }}
      onChange={vi.fn()}
    />,
  );
}

describe("profile editor — Parameters section", () => {
  it("collapses each parameter to one row instead of a stack of fields", () => {
    const html = markup();
    expect(html.match(/aria-expanded="false"/g)).toHaveLength(2);
    // The ten per-parameter fields are not rendered at rest — the whole point.
    expect(html).not.toContain("Value rewrite");
  });

  it("identifies a row by label, role and the reference you would type", () => {
    const html = markup();
    expect(html).toContain("Namespace");
    expect(html).toContain("filters");
    expect(html).toContain("{{.params.namespace}}");
    expect(html).toContain("resource.k8s.namespace");
    expect(html).toContain('title="Required"');
  });

  it("gives each parameter type its own glyph tone", () => {
    const html = markup();
    // `string` is slate, `list` is indigo — written-out classes, so a composed
    // class name (which Tailwind would never emit) fails here.
    expect(html).toContain("bg-slate-100");
    expect(html).toContain("bg-indigo-100");
  });

  it("counts parameters in the schema's own noun", () => {
    expect(markup()).toContain("2 parameters");
  });

  it("makes the add row the empty state when a profile has no parameters", () => {
    const html = markup({ ...DRAFT, params: [] } as unknown as ProfileWizardDraft);
    expect(html).not.toContain('aria-expanded="false"');
    expect(html).toContain("No parameters yet");
    expect(html).toContain("Add parameter");
    expect(html).toContain("A parameter turns a fixed query into a reusable one");
  });

  it("leaves the shared Advanced composition section on the plain defaults", () => {
    // ProfileSchemaSection is shared; presentation must come from the call site,
    // never a branch on `keys` inside the component.
    const html = renderToStaticMarkup(
      <ProfileSchemaSection
        draft={DRAFT}
        keys={["ignore"]}
        title="Advanced composition"
        description="Compose profiles."
        idPrefix="profile-advanced"
        onChange={vi.fn()}
      />,
    );
    expect(html).toContain("Advanced composition");
    expect(html).not.toContain("aria-expanded");
  });
});
