/**
 * @generated SignedSource<<227c50b7a4b5ed7f6e6d7c18d92ef7cf>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type AuthSignOutMutation$variables = Record<PropertyKey, never>;
export type AuthSignOutMutation$data = {
  readonly signOut: {
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly session: {
      readonly id: string;
      readonly revokedAt: any | null | undefined;
    } | null | undefined;
  };
};
export type AuthSignOutMutation = {
  response: AuthSignOutMutation$data;
  variables: AuthSignOutMutation$variables;
};

const node: ConcreteRequest = (function(){
var v0 = [
  {
    "alias": null,
    "args": null,
    "concreteType": "SignOutPayload",
    "kind": "LinkedField",
    "name": "signOut",
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
        "concreteType": "Session",
        "kind": "LinkedField",
        "name": "session",
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
            "name": "revokedAt",
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
    "name": "AuthSignOutMutation",
    "selections": (v0/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": [],
    "kind": "Operation",
    "name": "AuthSignOutMutation",
    "selections": (v0/*:: as any*/)
  },
  "params": {
    "cacheID": "312d4832159aee113f282a4213f2b0ff",
    "id": null,
    "metadata": {},
    "name": "AuthSignOutMutation",
    "operationKind": "mutation",
    "text": "mutation AuthSignOutMutation {\n  signOut {\n    errors {\n      code\n      message\n      field\n    }\n    session {\n      id\n      revokedAt\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "b78b626ba5a62a765a7a147043717aee";

export default node;
