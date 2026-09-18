# commons-db docs

An [Astro Starlight](https://starlight.astro.build) site covering `recordstore`, `recordresults`, the SQLite packages, and consuming commons-db from other applications.

```bash
cd docs
pnpm install
pnpm dev      # http://localhost:4321
pnpm build    # static site in dist/
pnpm linkcheck # every internal link and #anchor in dist/ resolves
```

Pages live in `src/content/docs/`, and the sidebar is declared in `astro.config.mjs`.

## Diagrams

The diagrams are [facet](https://github.com/flanksource/facet) React components in `src/diagrams/`, rendered live in the page as React islands (`@astrojs/react`, `client:load`). The arrows are measured in the browser, so a diagram has to hydrate.

Each diagram is one named export that renders only the diagram: no facet `Page`, `Section` or caption. The heading and any caption are ordinary Markdown in the page:

```mdx
import { SQLiteAppendFlow } from "../../../diagrams/recordstore/sqlite.tsx";

### Append, import and scan

<div class="facet-diagram not-content" role="img" aria-label="…">
  <SQLiteAppendFlow client:load />
</div>

A write holds the stream lock and runs in one transaction…
```

- `.facet-diagram` wraps one diagram. `not-content` keeps Starlight's prose spacing out of it. In the dark theme it gives the diagram a light backing, because the diagram palette is designed for a light page.
- `src/styles/facet-diagram.css` holds the diagrams' Tailwind utilities. `tailwind.config.mjs` scopes every utility under `.facet-diagram`, turns preflight off and mirrors facet's theme, so none of it restyles Starlight. It scans `src/diagrams/` and facet's `dist/components/`.

`@flanksource/facet` is installed from the local checkout (`file:../../facet`), because the diagram `Arrow` geometry, ports and labels aren't in a published release yet (npm's latest is 0.1.71). Build facet (`pnpm build` in `../../facet`) before `pnpm install` here. Once a facet release includes them, switch back to the registry version.

If `pnpm build` fails with `Named export 'parseCookie' not found … 'cookie' is a CommonJS module`, a stray `node_modules/cookie` in a parent directory, such as `~/node_modules`, is shadowing Astro's own. The prerender bundle imports `cookie` by bare name, and Node resolves it by walking up from `dist/`.
