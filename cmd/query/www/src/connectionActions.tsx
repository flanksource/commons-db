import type { FormActionsRenderer } from "@flanksource/clicky-ui";
import { ConnectionTestButton } from "./connectionTestButton";

// connectionFormActions adds a "Test" split-button (with a "Resolve values"
// dropdown option) to the connection create/edit form footer. Test probes the
// resolved URL's reachability; Resolve hydrates the draft (expanding secret:// and
// svc:// / ip:// / proxy:// / host:// workload URLs) and shows the resolved values
// with secrets masked. Both POST the live form value to the backend, so they work
// before the connection is saved. Other entities get no extra actions.
export const connectionFormActions: FormActionsRenderer = ({ value, action, canSubmit }) =>
  action.path.includes("/connection") ? (
    <ConnectionTestButton value={value} canSubmit={canSubmit} />
  ) : null;
