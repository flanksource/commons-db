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
  useOperations,
  type OperationsApiClient,
  type OperationResultFilterConfig,
  type ResultRenderContext,
} from "@flanksource/clicky-ui";
import { UiColumns } from "@flanksource/clicky-ui/icons";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { isValidElement, useMemo, useState, type ReactNode } from "react";
import { findProfileUpdateOperation } from "./profileUpdateOperation";
import {
  loadProfileDocument,
  profileColumns,
  promoteProfileColumns,
} from "./profileColumnPromotion";
import { ProfileJsonFieldPicker } from "./profileJsonFieldPicker";
import { discoverJsonFieldCandidates } from "./profileJsonFields";
import { PROFILES_QUERY_KEY } from "./profilesQuery";
import type { ProfileColumn } from "@flanksource/clicky-ui/profiles";

type ClickyTableNode = ClickyNode & {
  kind: "table";
  columns: ClickyColumn[];
  rows: ClickyRow[];
};

type DefaultResultProps = {
  download?: ClickyDownloadOptions;
  pagination?: ClickyProps["pagination"];
};

export type ProfileRowDetailsRemoteProps = Pick<
  ClickyProps,
  "download" | "pagination" | "url" | "dataFormat"
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
  const queryClient = useQueryClient();
  const { operations, isLoading: operationsLoading } = useOperations(client);
  const updateAction = findProfileUpdateOperation(operations);
  const [detailRow, setDetailRow] = useState<ClickyRow | null>(null);
  const [promotionOpen, setPromotionOpen] = useState(false);
  const [promotionError, setPromotionError] = useState<string>();
  const [saving, setSaving] = useState(false);
  const [success, setSuccess] = useState<string>();
  const candidates = useMemo(
    () => detailRow ? discoverJsonFieldCandidates(table.columns, detailRow) : [],
    [detailRow, table.columns],
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
    setDetailRow(null);
    setPromotionOpen(false);
    setPromotionError(undefined);
    setSuccess(undefined);
  };

  const saveColumns = async (additions: ProfileColumn[]) => {
    if (!updateAction) {
      setPromotionError("Profile update operations are unavailable");
      return;
    }
    setSaving(true);
    setPromotionError(undefined);
    try {
      await promoteProfileColumns({ client, updateAction, surfaceKey, additions });
      setPromotionOpen(false);
      setSuccess(`Added ${additions.length} ${additions.length === 1 ? "column" : "columns"}`);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["openapi-spec"] }),
        queryClient.invalidateQueries({ queryKey: ["operation-list"] }),
        queryClient.invalidateQueries({ queryKey: ["profile-editor", surfaceKey] }),
        queryClient.invalidateQueries({ queryKey: ["profile-json-fields", surfaceKey] }),
        queryClient.invalidateQueries({ queryKey: ["logs-entity-names"] }),
        queryClient.invalidateQueries({ queryKey: PROFILES_QUERY_KEY }),
      ]);
    } catch (cause) {
      setPromotionError(cause instanceof Error ? cause.message : "Profile update failed");
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <Clicky
        data={table}
        {...remoteProps}
        onTableRowClick={(row) => {
          setDetailRow(row);
          setPromotionOpen(false);
          setPromotionError(undefined);
          setSuccess(undefined);
        }}
        externalFilters={filterConfig?.filters}
        search={filterConfig?.search}
        timeRange={filterConfig?.timeRange}
        cellFilters={filterConfig?.cellFilters}
        onCellFilterChange={filterConfig?.onCellFilterChange}
        className="flex min-h-0 flex-1 flex-col"
      />
      <Modal
        open={detailRow != null}
        onClose={closeDetails}
        title={promotionOpen ? "Add JSON fields as columns" : detailRow ? profileRowDetailTitle(detailRow) : "Row details"}
        size="2xl"
      >
        {promotionOpen && profile.isLoading ? (
          <div className="text-sm text-muted-foreground">Loading profile…</div>
        ) : promotionOpen && profile.isError ? (
          <div className="space-y-4">
            <div className="text-sm text-destructive">
              {profile.error instanceof Error ? profile.error.message : "Unable to load profile"}
            </div>
            <Button type="button" variant="outline" onClick={() => setPromotionOpen(false)}>Back</Button>
          </div>
        ) : promotionOpen && profile.data ? (
          <ProfileJsonFieldPicker
            candidates={candidates}
            existingColumns={profile.data}
            saving={saving}
            error={promotionError}
            onCancel={() => setPromotionOpen(false)}
            onSave={saveColumns}
          />
        ) : detailRow ? (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center justify-between gap-2">
              {success ? <div className="text-sm text-green-700">{success}</div> : <span />}
              {candidates.length > 0 ? (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={operationsLoading || !updateAction}
                  title={updateAction ? "Add fields from JSON columns" : "Profile updates are unavailable"}
                  onClick={() => setPromotionOpen(true)}
                >
                  <Icon icon={UiColumns} className="size-4" />
                  Add JSON fields
                </Button>
              ) : null}
            </div>
            <ClickyNodeView node={profileRowDetailNode(detailRow, table.columns)} />
          </div>
        ) : null}
      </Modal>
    </>
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
