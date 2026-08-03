import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import {
  ProfileJsonFieldPicker,
  promotedColumnTypes,
} from "./profileJsonFieldPicker";
import type { JsonFieldCandidate } from "./profileJsonFields";

const candidates: JsonFieldCandidate[] = [
  {
    id: "metadata-object-user.email",
    source: "metadata",
    name: "user.email",
    type: "string",
    sample: "alice@example.com",
    cel: "email-expression",
  },
  {
    id: "tags-key-value-http.status_code",
    source: "tags",
    name: "http.status_code",
    type: "number",
    sample: 200,
    cel: "status-expression",
  },
];

describe("profile JSON field picker", () => {
  it("offers semantic display types for bytes and durations", () => {
    expect(promotedColumnTypes).toEqual([
      "string",
      "number",
      "boolean",
      "datetime",
      "duration",
      "bytes",
    ]);
  });

  it("groups candidates by source and marks configured expressions", () => {
    const html = renderToStaticMarkup(
      <ProfileJsonFieldPicker
        candidates={candidates}
        existingColumns={[{ name: "email", cel: "email-expression" }]}
        saving={false}
        onCancel={vi.fn()}
        onSave={vi.fn()}
      />,
    );

    expect(html).toContain("metadata");
    expect(html).toContain("tags");
    expect(html).toContain("user.email");
    expect(html).toContain("Configured as email");
    expect(html).toContain("http.status_code");
    expect(html).toContain("Review selected");
  });
});
