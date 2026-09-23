/**
 * @generated SignedSource<<468beaa0d54f0860333fea3fa0026f49>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type DeleteCustomProviderInput = {
  id: string;
};
export type CustomDeleteProviderMutation$variables = {
  input: DeleteCustomProviderInput;
};
export type CustomDeleteProviderMutation$data = {
  readonly deleteCustomProvider: {
    readonly deletedProviderID: string | null | undefined;
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
  };
};
export type CustomDeleteProviderMutation = {
  response: CustomDeleteProviderMutation$data;
  variables: CustomDeleteProviderMutation$variables;
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
    "concreteType": "DeleteCustomProviderPayload",
    "kind": "LinkedField",
    "name": "deleteCustomProvider",
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
        "name": "deletedProviderID",
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
    "name": "CustomDeleteProviderMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "CustomDeleteProviderMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "986ed631dc983eb06bce4de392e44fd7",
    "id": null,
    "metadata": {},
    "name": "CustomDeleteProviderMutation",
    "operationKind": "mutation",
    "text": "mutation CustomDeleteProviderMutation(\n  $input: DeleteCustomProviderInput!\n) {\n  deleteCustomProvider(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    deletedProviderID\n  }\n}\n"
  }
};
})();

(node as any).hash = "da755e08e36145b5f7dd5c1c02153a60";

export default node;
