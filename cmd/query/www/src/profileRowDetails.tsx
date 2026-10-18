import {
  Clicky,
  ClickyNodeView,
  type ClickyColumn,
  type ClickyDownloadOptions,
  type ClickyNode,
  type ClickyProps,
  type ClickyRow,
} from "@flanksource/clicky-ui/clicky";
import {
  Button,
  Icon,
  Modal,
  type OperationsApiClient,
  type OperationResultFilterConfig,
  type ResultRenderContext,
} from "@flanksource/clicky-ui";
import { UiColumns } from "@flanksource/clicky-ui/icons";
import { useQuery } from "@tanstack/react-query";
import { isValidElement, useMemo, useState, type ReactNode } from "react";
import { loadProfileDocument, profileColumns } from "./profileColumnPromotion";
import { ProfileJsonFieldPicker } from "./profileJsonFieldPicker";
import { discoverJsonFieldCandidates } from "./profileJsonFields";
import { useColumnPromotion, type ColumnPromotion } from "./useColumnPromotion";
import type { ProfileColumn } from "@flanksource/clicky-ui/profiles";

type ClickyTableNode = ClickyNode & {
  kind: "table";
  columns: ClickyColumn[];
  rows: ClickyRow[];
};

type DefaultResultProps = {
  download?: ClickyDownloadOptions;
  pagination?: ClickyProps["pagination"];
  // The default view takes the sort as one control — the current value and the
  // setter together — where Clicky takes them as two separate props.
  sort?: {
    value: ClickyProps["sort"];
    onChange: NonNullable<ClickyProps["onSortChange"]>;
  };
};

export type ProfileRowDetailsRemoteProps = Pick<
  ClickyProps,
  "download" | "pagination" | "sort" | "onSortChange" | "url" | "dataFormat"
>;

// The URL travels with the rows so the view switcher and the export menu have
// an endpoint to act on. The rows themselves came from that endpoint's clicky
// representation, which is what dataFormat says — without it the table is
// fetched a second time to be handed back the bytes it is already rendering.
export function profileRowDetailsRemoteProps(
  response: { requestUrl?: string; contentType?: string } | null,
  defaultView: ReactNode,
): ProfileRowDetailsRemoteProps {
  const resultProps = isValidElement<DefaultResultProps>(defaultView)
    ? defaultView.props
    : {};
  const contentType = (response?.contentType?.split(";")[0] ?? "").trim();
  const servedAsClicky =
    contentType === "application/clicky+json" ||
    contentType === "application/json+clicky";
  return {
    ...(response?.requestUrl ? { url: response.requestUrl } : {}),
    ...(response?.requestUrl && servedAsClicky
      ? { dataFormat: "clicky" as const }
      : {}),
    ...(resultProps.download ? { download: resultProps.download } : {}),
    ...(resultProps.pagination ? { pagination: resultProps.pagination } : {}),
    // A server-paged table sorts through the query rather than in the browser,
    // so dropping this is what left its headers inert. It arrives as one
    // control and is handed on as the two props the table takes.
    ...(resultProps.sort
      ? { sort: resultProps.sort.value, onSortChange: resultProps.sort.onChange }
      : {}),
  };
}

export function profileRowDetailTitle(row: ClickyRow): string {
  for (const key of ["operationName", "message", "traceID", "spanID"]) {
    const value = row.cells[key]?.plain?.trim();
    if (value) return `${value} details`;
  }
  return "Row details";
}

export function profileRowDetailNode(
  row: ClickyRow,
  columns: ClickyColumn[],
): ClickyNode {
  const labels = new Map(columns.map((column) => [column.name, column.label]));
  return {
    kind: "map",
    fields: Object.entries(row.cells).map(([name, value]) => ({
      name,
      label: labels.get(name) ?? name,
      value,
    })),
  };
}

export function profileRowDetailsResult(
  context: ResultRenderContext,
  client: OperationsApiClient,
  surfaceKey: string,
): ReactNode {
  const table = clickyTableNode(context.response?.parsed);
  if (!table) return context.defaultView;
  return (
    <ProfileRowDetailsTable
      table={table}
      remoteProps={profileRowDetailsRemoteProps(context.response, context.defaultView)}
      {...(context.filterConfig ? { filterConfig: context.filterConfig } : {})}
      client={client}
      surfaceKey={surfaceKey}
    />
  );
}

function ProfileRowDetailsTable({
  table,
  remoteProps,
  filterConfig,
  client,
  surfaceKey,
}: {
  table: ClickyTableNode;
  remoteProps: ProfileRowDetailsRemoteProps;
  filterConfig?: OperationResultFilterConfig;
  client: OperationsApiClient;
  surfaceKey: string;
}) {
  const [detailRow, setDetailRow] = useState<ClickyRow | null>(null);
  return (
    <>
      <Clicky
        data={table}
        {...remoteProps}
        onTableRowClick={setDetailRow}
        externalFilters={filterConfig?.filters}
        search={filterConfig?.search}
        timeRange={filterConfig?.timeRange}
        cellFilters={filterConfig?.cellFilters}
        onCellFilterChange={filterConfig?.onCellFilterChange}
        className="flex min-h-0 flex-1 flex-col"
      />
      <RowDetailsModal
        columns={table.columns}
        detailRow={detailRow}
        client={client}
        surfaceKey={surfaceKey}
        onClose={() => setDetailRow(null)}
      />
    </>
  );
}

function RowDetailsModal({
  columns,
  detailRow,
  client,
  surfaceKey,
  onClose,
}: {
  columns: ClickyColumn[];
  detailRow: ClickyRow | null;
  client: OperationsApiClient;
  surfaceKey: string;
  onClose: () => void;
}) {
  const [promotionOpen, setPromotionOpen] = useState(false);
  const promotion = useColumnPromotion({
    client,
    surfaceKey,
    onSaved: () => setPromotionOpen(false),
  });
  const candidates = useMemo(
    () => (detailRow ? discoverJsonFieldCandidates(columns, detailRow) : []),
    [detailRow, columns],
  );
  const profile = useQuery({
    queryKey: ["profile-json-fields", surfaceKey],
    enabled: promotionOpen,
    queryFn: async () => {
      const document = await loadProfileDocument(surfaceKey);
      return profileColumns(document);
    },
    retry: 0,
  });
  const closeDetails = () => {
    setPromotionOpen(false);
    promotion.reset();
    onClose();
  };
  const title = promotionOpen
    ? "Add JSON fields as columns"
    : detailRow
      ? profileRowDetailTitle(detailRow)
      : "Row details";
  return (
    <Modal open={detailRow != null} onClose={closeDetails} title={title} size="2xl">
      {detailRow ? (
        <RowDetailsBody
          detailRow={detailRow}
          columns={columns}
          candidates={candidates}
          promotionOpen={promotionOpen}
          onPromotionOpenChange={setPromotionOpen}
          profile={profile}
          promotion={promotion}
        />
      ) : null}
    </Modal>
  );
}

function RowDetailsBody({
  detailRow,
  columns,
  candidates,
  promotionOpen,
  onPromotionOpenChange,
  profile,
  promotion,
}: {
  detailRow: ClickyRow;
  columns: ClickyColumn[];
  candidates: ReturnType<typeof discoverJsonFieldCandidates>;
  promotionOpen: boolean;
  onPromotionOpenChange: (open: boolean) => void;
  profile: { isLoading: boolean; isError: boolean; error: unknown; data?: ProfileColumn[] };
  promotion: ColumnPromotion;
}) {
  if (promotionOpen && profile.isLoading) {
    return <div className="text-sm text-muted-foreground">Loading profile…</div>;
  }
  if (promotionOpen && profile.isError) {
    return (
      <div className="space-y-4">
        <div className="text-sm text-destructive">
          {profile.error instanceof Error ? profile.error.message : "Unable to load profile"}
        </div>
        <Button type="button" variant="outline" onClick={() => onPromotionOpenChange(false)}>Back</Button>
      </div>
    );
  }
  if (promotionOpen && profile.data) {
    return (
      <ProfileJsonFieldPicker
        candidates={candidates}
        existingColumns={profile.data}
        saving={promotion.saving}
        error={promotion.error}
        onCancel={() => onPromotionOpenChange(false)}
        onSave={promotion.save}
      />
    );
  }
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        {promotion.success ? <div className="text-sm text-green-700">{promotion.success}</div> : <span />}
        {candidates.length > 0 ? (
          <AddJsonFieldsButton onClick={() => onPromotionOpenChange(true)} promotion={promotion} />
        ) : null}
      </div>
      <ClickyNodeView node={profileRowDetailNode(detailRow, columns)} />
    </div>
  );
}

function AddJsonFieldsButton({
  onClick,
  promotion,
}: {
  onClick: () => void;
  promotion: ColumnPromotion;
}) {
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      disabled={promotion.operationsLoading || !promotion.updateAction}
      title={promotion.updateAction ? "Add fields from JSON columns" : "Profile updates are unavailable"}
      onClick={onClick}
    >
      <Icon icon={UiColumns} className="size-4" />
      Add JSON fields
    </Button>
  );
}

function clickyTableNode(value: unknown): ClickyTableNode | null {
  if (!value || typeof value !== "object") return null;
  const document = value as { node?: ClickyNode };
  const node = document.node;
  if (node?.kind !== "table" || !Array.isArray(node.columns) || !Array.isArray(node.rows)) {
    return null;
  }
  return node as ClickyTableNode;
}
