import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import react from "@astrojs/react";

export default defineConfig({
  integrations: [
    react(),
    starlight({
      customCss: ["./src/styles/layout.css", "./src/styles/facet-diagram.css"],
      title: "commons-db",
      description: "Record streams, typed result profiles and SQLite storage for Flanksource Go apps.",
      social: [{ icon: "github", label: "GitHub", href: "https://github.com/flanksource/commons-db" }],
      editLink: { baseUrl: "https://github.com/flanksource/commons-db/edit/main/docs/" },
      sidebar: [
        {
          label: "Start here",
          items: [
            { label: "Introduction", slug: "index" },
            { label: "Consuming commons-db", slug: "start/consuming" },
            { label: "Tutorial: capture and serve a stream", slug: "start/tutorial" },
          ],
        },
        {
          label: "recordstore",
          items: [
            { label: "Concepts", slug: "recordstore/concepts" },
            { label: "Kinds and schemas", slug: "recordstore/kinds" },
            { label: "Backends", slug: "recordstore/backends" },
            { label: "Settings and routing", slug: "recordstore/settings" },
            { label: "Following and indexing", slug: "recordstore/following" },
            { label: "Probes: cursor sources", slug: "recordstore/probes" },
            { label: "Writing a backend", slug: "recordstore/custom-backend" },
            { label: "Worked examples", slug: "recordstore/examples" },
          ],
        },
        {
          label: "recordresults",
          items: [
            { label: "Opening a result store", slug: "recordresults/open" },
            { label: "Result types", slug: "recordresults/result-types" },
            { label: "Views", slug: "recordresults/views" },
            { label: "Serving over HTTP", slug: "recordresults/serving" },
            { label: "Stream refs", slug: "recordresults/stream-refs" },
          ],
        },
        {
          label: "SQLite",
          items: [
            { label: "sqlite.DB", slug: "sqlite/database" },
            { label: "sqlitetable", slug: "sqlite/sqlitetable" },
            { label: "The sqlite record file", slug: "sqlite/record-file" },
          ],
        },
      ],
    }),
  ],
});
