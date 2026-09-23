/**
 * @generated SignedSource<<58b59ffc7efced733410eea6c2aeb369>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type RevokeProviderConnectionInput = {
  id: string;
};
export type ConnectionsRevokeProviderMutation$variables = {
  input: RevokeProviderConnectionInput;
};
export type ConnectionsRevokeProviderMutation$data = {
  readonly revokeProviderConnection: {
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly revokedConnectionID: string | null | undefined;
  };
};
export type ConnectionsRevokeProviderMutation = {
  response: ConnectionsRevokeProviderMutation$data;
  variables: ConnectionsRevokeProviderMutation$variables;
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
    "concreteType": "RevokeProviderConnectionPayload",
    "kind": "LinkedField",
    "name": "revokeProviderConnection",
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
        "kind": "ScalarField",
        "name": "revokedConnectionID",
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
    "name": "ConnectionsRevokeProviderMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "ConnectionsRevokeProviderMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "0d59fca46c21ceba8a757baee08174e1",
    "id": null,
    "metadata": {},
    "name": "ConnectionsRevokeProviderMutation",
    "operationKind": "mutation",
    "text": "mutation ConnectionsRevokeProviderMutation(\n  $input: RevokeProviderConnectionInput!\n) {\n  revokeProviderConnection(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    revokedConnectionID\n  }\n}\n"
  }
};
})();

(node as any).hash = "af904ab94405da03ee7649e718a722b7";

export default node;
