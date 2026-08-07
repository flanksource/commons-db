import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const comboboxCalls = vi.hoisted(() => [] as Record<string, unknown>[]);

vi.mock("@flanksource/clicky-ui", () => ({
  Combobox: (props: Record<string, unknown>) => {
    comboboxCalls.push(props);
    return <input aria-label={String(props.ariaLabel)} />;
  },
}));

import { ValuesCombobox } from "./esValueCombobox";

describe("ValuesCombobox", () => {
  beforeEach(() => {
    comboboxCalls.length = 0;
  });

  it("keeps an OpenSearch terms operand creatable while selecting multiple values", () => {
    const onChange = vi.fn();
    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <ValuesCombobox
          label="Values"
          lookup={{
            key: "service.name",
            fetch: async () => ({ values: [], total: 0, scoped: true }),
          }}
          values={["payments"]}
          onChange={onChange}
        />
      </QueryClientProvider>,
    );

    expect(comboboxCalls).toHaveLength(1);
    expect(comboboxCalls[0]).toMatchObject({
      multiple: true,
      variant: "tags",
      allowCustomValue: true,
      value: ["payments"],
    });

    const change = comboboxCalls[0]?.onChange as
      | ((next: string[]) => void)
      | undefined;
    expect(change).toBeDefined();
    change?.(["payments", "custom-service"]);
    expect(onChange).toHaveBeenCalledWith(["payments", "custom-service"]);
  });
});
