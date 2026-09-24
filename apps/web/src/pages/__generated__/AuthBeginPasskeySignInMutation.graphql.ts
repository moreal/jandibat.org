/**
 * @generated SignedSource<<96f3e141c65dc414e01e9eae8f1b90c0>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type AuthBeginPasskeySignInMutation$variables = Record<PropertyKey, never>;
export type AuthBeginPasskeySignInMutation$data = {
  readonly beginPasskeySignIn: {
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly options: {
      readonly ceremonyID: string;
      readonly expiresAt: any;
      readonly publicKeyJSON: string;
    } | null | undefined;
  };
};
export type AuthBeginPasskeySignInMutation = {
  response: AuthBeginPasskeySignInMutation$data;
  variables: AuthBeginPasskeySignInMutation$variables;
};

const node: ConcreteRequest = (function(){
var v0 = [
  {
    "alias": null,
    "args": null,
    "concreteType": "BeginPasskeySignInPayload",
    "kind": "LinkedField",
    "name": "beginPasskeySignIn",
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
        "concreteType": "PasskeyOptions",
        "kind": "LinkedField",
        "name": "options",
        "plural": false,
        "selections": [
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "ceremonyID",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "publicKeyJSON",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "expiresAt",
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
    "argumentDefinitions": [],
    "kind": "Fragment",
    "metadata": null,
    "name": "AuthBeginPasskeySignInMutation",
    "selections": (v0/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": [],
    "kind": "Operation",
    "name": "AuthBeginPasskeySignInMutation",
    "selections": (v0/*:: as any*/)
  },
  "params": {
    "cacheID": "e2999c30a74cc57203c49fa890503ef2",
    "id": null,
    "metadata": {},
    "name": "AuthBeginPasskeySignInMutation",
    "operationKind": "mutation",
    "text": "mutation AuthBeginPasskeySignInMutation {\n  beginPasskeySignIn {\n    errors {\n      code\n      message\n      field\n    }\n    options {\n      ceremonyID\n      publicKeyJSON\n      expiresAt\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "4ee59553447e50cb9ea1d505be183813";

export default node;
