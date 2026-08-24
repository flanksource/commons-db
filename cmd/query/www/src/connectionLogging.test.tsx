import { isValidElement, type ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type {
  ConnectionLoggingCapability,
  ConnectionLoggingPolicyProps,
  FieldControl,
  PostExtensionContext,
} from "@flanksource/clicky-ui";
import { connectionLoggingFormExtensions } from "./connectionLogging";

const capability: ConnectionLoggingCapability = {
  family: "http",
  slowThreshold: "1s",
  thresholdLabel: "Slow threshold",
  events: [
    {
      event: "error",
      property: "log.level.error",
      label: "Errors",
      description: "Failed requests.",
      default: "error",
      captures: ["error"],
      example: { level: "error" },
      prettyExample: "[http/search] ERROR >=[82ms] [rows:0] request timed out",
    },
    {
      event: "http",
      property: "log.level.http",
      label: "Access summary",
      description: "Method and status.",
      default: "debug",
      captures: ["method", "status"],
      example: { level: "debug", status: 200 },
      prettyExample:
        "[http/search] POST https://api.example.test/_search 200 OK 86ms 512B",
    },
  ],
};

function field(): FieldControl {
  return {
    key: "logging",
    kind: "object",
    label: "Logging",
    required: false,
    schema: {
      "x-clicky-component": "connection-logging-policy",
      "x-clicky-logging": capability,
    },
    value: undefined,
    onChange: vi.fn(),
  };
}

describe("connectionLoggingFormExtensions", () => {
  it("turns the synthetic schema field into a full-width display surface", () => {
    const [pre] = connectionLoggingFormExtensions.pre;
    const transformed = pre?.(field(), {
      key: "logging",
      prop: field().schema,
      value: undefined,
    });

    expect(transformed).toMatchObject({
      kind: "display",
      displayVariant: "spacer",
      colSpan: "full",
    });
  });

  it("writes overrides into the root connection properties map", () => {
    const onRootChange = vi.fn();
    const ctx: PostExtensionContext = {
      rootValue: {
        name: "search",
        type: "opensearch",
        properties: { authType: "none" },
      },
      onRootChange,
    };
    const [post] = connectionLoggingFormExtensions.post;
    const nodes = post?.(
      field(),
      { label: <span>Logging</span>, value: null },
      ctx,
    );
    expect(renderToStaticMarkup(<>{nodes?.value}</>)).toContain(
      "Access summary",
    );

    type LoggingFieldProps = {
      field: FieldControl;
      ctx: PostExtensionContext;
    };
    if (!isValidElement<LoggingFieldProps>(nodes?.value)) {
      throw new Error("connection logging extension did not return a field");
    }
    const renderField = nodes.value.type as (
      props: LoggingFieldProps,
    ) => ReactElement<ConnectionLoggingPolicyProps>;
    const editor = renderField(nodes.value.props);

    editor.props.onChange({
      authType: "none",
      "log.level.http": "info",
    });
    expect(onRootChange).toHaveBeenCalledExactlyOnceWith({
      name: "search",
      type: "opensearch",
      properties: { authType: "none", "log.level.http": "info" },
    });
  });
});
