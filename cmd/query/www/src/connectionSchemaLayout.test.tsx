/** @vitest-environment jsdom */

import { JsonSchemaForm, type JsonSchemaObject } from "@flanksource/clicky-ui";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import connectionSchema from "../../../../schemas/connection.json";

const FULL_ROW = "1 / -1";

// The grid cell a field's label sits in: JsonSchemaForm wraps each field of an
// `x-columns` object in an element carrying its inline grid-column.
function gridCellOf(fieldId: string): HTMLElement {
  const label = document.querySelector<HTMLElement>(`label[for='${fieldId}']`);
  if (!label) throw new Error(`no label for ${fieldId}`);
  const cell = label.closest<HTMLElement>("[style*='grid-column']");
  if (!cell) throw new Error(`no grid-column cell around ${fieldId}`);
  return cell;
}

describe("served connection schema layout", () => {
  let root: Root;

  beforeEach(() => {
    document.body.innerHTML = '<div id="root"></div>';
    root = createRoot(document.getElementById("root")!);
  });

  afterEach(() => {
    act(() => root.unmount());
  });

  it.each(["opensearch", "http", "prometheus"])(
    "puts a %s connection's basic-auth username and password on their own rows",
    async (type) => {
      const value = { type, name: "example", properties: { authType: "basic" } };
      await act(async () => {
        root.render(
          <JsonSchemaForm
            schema={connectionSchema as unknown as JsonSchemaObject}
            value={value}
            onChange={() => {}}
            showPreferencesMenu={false}
          />,
        );
      });

      expect({
        username: gridCellOf("jsf-properties-username").style.gridColumn,
        password: gridCellOf("jsf-properties-password").style.gridColumn,
      }).toEqual({ username: FULL_ROW, password: FULL_ROW });
    },
  );
});
