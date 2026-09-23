/**
 * @generated SignedSource<<76c0efbee48bdb20bda99d3064a0fb58>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type RotateCustomProviderKeyInput = {
  id: string;
};
export type CustomRotateProviderKeyMutation$variables = {
  input: RotateCustomProviderKeyInput;
};
export type CustomRotateProviderKeyMutation$data = {
  readonly rotateCustomProviderKey: {
    readonly createdAt: any | null | undefined;
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly ingestionKey: string | null | undefined;
  };
};
export type CustomRotateProviderKeyMutation = {
  response: CustomRotateProviderKeyMutation$data;
  variables: CustomRotateProviderKeyMutation$variables;
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
    "concreteType": "RotateCustomProviderKeyPayload",
    "kind": "LinkedField",
    "name": "rotateCustomProviderKey",
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
        "name": "ingestionKey",
        "storageKey": null
      },
      {
        "alias": null,
        "args": null,
        "kind": "ScalarField",
        "name": "createdAt",
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
    "name": "CustomRotateProviderKeyMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "CustomRotateProviderKeyMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "313c6e8a3f4af3551ec389d808c520ff",
    "id": null,
    "metadata": {},
    "name": "CustomRotateProviderKeyMutation",
    "operationKind": "mutation",
    "text": "mutation CustomRotateProviderKeyMutation(\n  $input: RotateCustomProviderKeyInput!\n) {\n  rotateCustomProviderKey(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    ingestionKey\n    createdAt\n  }\n}\n"
  }
};
})();

(node as any).hash = "2fc61ae9f0a56e67e0fc41eed848d2f4";

export default node;
