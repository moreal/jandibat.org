/**
 * @generated SignedSource<<f16ae805844213fc2582ebd449929ed8>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type CreateCustomProviderInput = {
  allowedActions?: ReadonlyArray<string> | null | undefined;
  description?: string | null | undefined;
  name: string;
  slug: string;
  subjectID: string;
};
export type CustomCreateProviderMutation$variables = {
  input: CreateCustomProviderInput;
};
export type CustomCreateProviderMutation$data = {
  readonly createCustomProvider: {
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly ingestionKey: string | null | undefined;
    readonly provider: {
      readonly id: string;
    } | null | undefined;
  };
};
export type CustomCreateProviderMutation = {
  response: CustomCreateProviderMutation$data;
  variables: CustomCreateProviderMutation$variables;
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
    "concreteType": "CreateCustomProviderPayload",
    "kind": "LinkedField",
    "name": "createCustomProvider",
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
        "concreteType": "CustomProvider",
        "kind": "LinkedField",
        "name": "provider",
        "plural": false,
        "selections": [
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "id",
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
    "name": "CustomCreateProviderMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "CustomCreateProviderMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "ef75488e39967fdfba08d517c7a7f069",
    "id": null,
    "metadata": {},
    "name": "CustomCreateProviderMutation",
    "operationKind": "mutation",
    "text": "mutation CustomCreateProviderMutation(\n  $input: CreateCustomProviderInput!\n) {\n  createCustomProvider(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    provider {\n      id\n    }\n    ingestionKey\n  }\n}\n"
  }
};
})();

(node as any).hash = "ccfb4731ce02d92bc67e0bae26ec35ec";

export default node;
