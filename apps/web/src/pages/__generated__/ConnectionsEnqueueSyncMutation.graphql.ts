/**
 * @generated SignedSource<<04b8f5f652fb511426246d88dd1be26e>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type FetchFailurePolicy = "KEEP_STALE" | "PURGE" | "%future added value";
export type EnqueueManualSyncInput = {
  connectionID: string;
  failurePolicy?: FetchFailurePolicy | null | undefined;
  force?: boolean | null | undefined;
  from?: any | null | undefined;
  idempotencyKey: string;
  to?: any | null | undefined;
};
export type ConnectionsEnqueueSyncMutation$variables = {
  input: EnqueueManualSyncInput;
};
export type ConnectionsEnqueueSyncMutation$data = {
  readonly enqueueManualSync: {
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly job: {
      readonly id: string;
      readonly status: string;
    } | null | undefined;
  };
};
export type ConnectionsEnqueueSyncMutation = {
  response: ConnectionsEnqueueSyncMutation$data;
  variables: ConnectionsEnqueueSyncMutation$variables;
};

const node: ConcreteRequest = (function(){
var v0 = [
  {
    "defaultValue": null,
    "kind": "LocalArgument",
    "name": "input"
  }
],
v1 = [
  {
    "alias": null,
    "args": [
      {
        "kind": "Variable",
        "name": "input",
        "variableName": "input"
      }
    ],
    "concreteType": "EnqueueManualSyncPayload",
    "kind": "LinkedField",
    "name": "enqueueManualSync",
    "plural": false,
    "selections": [
      {
        "alias": null,
        "args": null,
        "concreteType": "MutationError",
        "kind": "LinkedField",
        "name": "errors",
        "plural": true,
        "selections": [
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "code",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "message",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "field",
            "storageKey": null
          }
        ],
        "storageKey": null
      },
      {
        "alias": null,
        "args": null,
        "concreteType": "SyncJob",
        "kind": "LinkedField",
        "name": "job",
        "plural": false,
        "selections": [
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "id",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "status",
            "storageKey": null
          }
        ],
        "storageKey": null
      }
    ],
    "storageKey": null
  }
];
return {
  "fragment": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Fragment",
    "metadata": null,
    "name": "ConnectionsEnqueueSyncMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "ConnectionsEnqueueSyncMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "b74f4983925fff7bd4f8170e29b7d489",
    "id": null,
    "metadata": {},
    "name": "ConnectionsEnqueueSyncMutation",
    "operationKind": "mutation",
    "text": "mutation ConnectionsEnqueueSyncMutation(\n  $input: EnqueueManualSyncInput!\n) {\n  enqueueManualSync(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    job {\n      id\n      status\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "b209bd9b33c0add6934192931e6fa102";

export default node;
