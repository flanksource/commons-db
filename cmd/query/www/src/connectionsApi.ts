import { createOperationsApiClient } from "@flanksource/clicky-ui";
import { withDebugFetch } from "@flanksource/clicky-ui/devtools";
import { mountPath } from "./mount";

// The connections service is mounted at /api/v1 under the path the server
// mounted the app at.
export const connectionsApiBase = `${mountPath}/api/v1`;

// clicky-ui's connection hooks (useConnectionSchema) address the service by
// paths relative to the client they are handed, so they need a client rooted
// at the connections service rather than the explorer's mount-rooted one.
export const connectionsClient = createOperationsApiClient({
  baseUrl: connectionsApiBase,
  fetch: withDebugFetch(),
});
