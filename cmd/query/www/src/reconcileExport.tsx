import { useState } from "react";
import {
  CLICKY_FORMAT_OPTIONS,
  FormatOptionsDropdown,
  type FormatOption,
} from "@flanksource/clicky-ui";

export function reconcileExportOptions(formats: string[]): FormatOption[] {
  const advertised = new Set(formats);
  return CLICKY_FORMAT_OPTIONS.filter((option) => advertised.has(option.value));
}

export function reconcileExportURL(
  requestUrl: string,
  format: string,
  label: string,
  outcome: string,
  nonce = Date.now(),
): string {
  const extension = EXPORT_EXTENSIONS[format];
  if (!extension) throw new Error(`Unsupported reconcile export format ${format}`);
  const base = typeof window === "undefined" ? "http://query.local" : window.location.origin;
  const url = new URL(requestUrl, base);
  url.searchParams.set("outcome", outcome);
  url.searchParams.set("format", format);
  url.searchParams.set("filename", `${sanitizeFilenameStem(label)}${extension}`);
  url.searchParams.set("_download", String(nonce));
  return isAbsoluteURL(requestUrl) ? url.toString() : `${url.pathname}${url.search}${url.hash}`;
}

export function ReconcileExport({
  requestUrl,
  formats,
  label,
  outcome,
}: {
  requestUrl: string;
  formats: string[];
  label: string;
  outcome: string;
}) {
  const options = reconcileExportOptions(formats);
  const initialFormat = options.some((option) => option.value === "csv")
    ? "csv"
    : options[0]?.value ?? "json";
  const [format, setFormat] = useState(initialFormat);
  const [downloadURL, setDownloadURL] = useState("");
  if (options.length === 0) return null;

  const selectFormat = (next: string) => {
    setFormat(next);
    setDownloadURL(reconcileExportURL(requestUrl, next, label, outcome));
  };

  return (
    <>
      <FormatOptionsDropdown
        value={format}
        onChange={selectFormat}
        options={options}
        label="Export"
        size="sm"
      />
      {downloadURL && (
        <iframe
          title="Reconcile download target"
          src={downloadURL}
          aria-hidden="true"
          tabIndex={-1}
          className="hidden"
        />
      )}
    </>
  );
}

const EXPORT_EXTENSIONS: Record<string, string> = {
  json: ".json",
  yaml: ".yaml",
  csv: ".csv",
  markdown: ".md",
  html: ".html",
  pdf: ".pdf",
  excel: ".xlsx",
};

function sanitizeFilenameStem(value: string): string {
  return value
    .trim()
    .replace(/\.[a-z0-9]+$/i, "")
    .replace(/[^a-z0-9._-]+/gi, "-")
    .replace(/-+/g, "-")
    .replace(/^[._-]+|[._-]+$/g, "") || "reconcile-results";
}

function isAbsoluteURL(url: string): boolean {
  return /^[a-z][a-z\d+\-.]*:/i.test(url) || url.startsWith("//");
}
