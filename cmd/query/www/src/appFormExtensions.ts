import { createUnitFormExtensions } from "@flanksource/clicky-ui";
import {
  connectionLoggingFormExtensions,
  createConnectionFormActions,
} from "@flanksource/clicky-ui/connections";
import {
  celEditorFormExtensions,
  esQueryBuilderFormExtensions,
  profileBuilderFormExtensions,
  processorPipelineFormExtensions,
} from "@flanksource/clicky-ui/profiles";
import { mountPath } from "./mount";
import { secretFormExtensions } from "./secretKeySelector";
import { namespaceFormExtensions } from "./namespacePicker";
import { esParamOptionsFormExtensions } from "./esParamOptions";
import { jsonPathFormExtensions } from "./jsonPathPicker";
import { configureProfileConnectionForm } from "./profileConnectionMapping";

const unitFormExtensions = createUnitFormExtensions();

// The connections service is mounted at /api/v1 under the path the server
// mounted the app at.
export const connectionsApiBase = `${mountPath}/api/v1`;
export const connectionFormActions = createConnectionFormActions({ apiBase: connectionsApiBase });

// Compose the form extensions: the namespace picker, the secret/workload url
// selector (which reads the selected namespace from the form's root value), the
// profile query builder, and the structured OpenSearch filter builder that
// mounts on provider.options.search in both the create and the edit form.
// Exported so main.tsx can hand the same list to configureProfiles: the profile
// editor renders its own JsonSchemaForms inside clicky-ui, far from this app's
// EntityExplorerApp, and without them every x-clicky-component in the profile
// schema — the CEL editor, the processor pipeline, the namespace and secret
// pickers — falls back to a plain control.
export const formExtensions = {
  pre: [
    ...unitFormExtensions.pre,
    ...esParamOptionsFormExtensions.pre,
    ...connectionLoggingFormExtensions.pre,
  ],
  post: [
    ...namespaceFormExtensions.post,
    ...secretFormExtensions.post,
    ...profileBuilderFormExtensions.post,
    ...esQueryBuilderFormExtensions.post,
    ...esParamOptionsFormExtensions.post,
    ...jsonPathFormExtensions.post,
    ...celEditorFormExtensions.post,
    ...processorPipelineFormExtensions.post,
    ...connectionLoggingFormExtensions.post,
  ],
};

configureProfileConnectionForm({
  formPre: formExtensions.pre,
  formPost: formExtensions.post,
  footerActions: connectionFormActions,
});
