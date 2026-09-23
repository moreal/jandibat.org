/**
 * @generated SignedSource<<b053cb2c6c23a725e6966248ec75ab22>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type CustomProviderStatus = "ACTIVE" | "DISABLED" | "%future added value";
export type UpdateCustomProviderInput = {
  allowedActions?: ReadonlyArray<string> | null | undefined;
  clearDescription?: boolean | null | undefined;
  description?: string | null | undefined;
  id: string;
  name?: string | null | undefined;
  status?: CustomProviderStatus | null | undefined;
};
export type CustomUpdateProviderMutation$variables = {
  input: UpdateCustomProviderInput;
};
export type CustomUpdateProviderMutation$data = {
  readonly updateCustomProvider: {
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly provider: {
      readonly allowedActions: ReadonlyArray<string>;
      readonly description: string;
      readonly id: string;
      readonly name: string;
      readonly status: string;
      readonly updatedAt: any;
    } | null | undefined;
  };
};
export type CustomUpdateProviderMutation = {
  response: CustomUpdateProviderMutation$data;
  variables: CustomUpdateProviderMutation$variables;
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
    "concreteType": "UpdateCustomProviderPayload",
    "kind": "LinkedField",
    "name": "updateCustomProvider",
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
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "name",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "description",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "status",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "allowedActions",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "updatedAt",
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
    "name": "CustomUpdateProviderMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "CustomUpdateProviderMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "9e0f0afd3e5bf3be98e6b1affa9c7c63",
    "id": null,
    "metadata": {},
    "name": "CustomUpdateProviderMutation",
    "operationKind": "mutation",
    "text": "mutation CustomUpdateProviderMutation(\n  $input: UpdateCustomProviderInput!\n) {\n  updateCustomProvider(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    provider {\n      id\n      name\n      description\n      status\n      allowedActions\n      updatedAt\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "3b9393fd76ceda80163959cfc115b8bb";

export default node;
