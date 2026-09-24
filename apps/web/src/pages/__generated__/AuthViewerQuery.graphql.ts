/**
 * @generated SignedSource<<f948e63155b9447614cdd8db0ae5f0b0>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type AuthViewerQuery$variables = Record<PropertyKey, never>;
export type AuthViewerQuery$data = {
  readonly viewer: {
    readonly currentSession: {
      readonly createdAt: any;
      readonly expiresAt: any;
      readonly id: string;
      readonly revokedAt: any | null | undefined;
    };
    readonly user: {
      readonly emailVerifiedAt: any | null | undefined;
      readonly id: string;
      readonly primaryEmail: string;
      readonly status: string;
    };
  } | null | undefined;
};
export type AuthViewerQuery = {
  response: AuthViewerQuery$data;
  variables: AuthViewerQuery$variables;
};

const node: ConcreteRequest = (function(){
var v0 = {
  "alias": null,
  "args": null,
  "kind": "ScalarField",
  "name": "id",
  "storageKey": null
},
v1 = [
  {
    "alias": null,
    "args": null,
    "concreteType": "Viewer",
    "kind": "LinkedField",
    "name": "viewer",
    "plural": false,
    "selections": [
      {
        "alias": null,
        "args": null,
        "concreteType": "UserProfile",
        "kind": "LinkedField",
        "name": "user",
        "plural": false,
        "selections": [
          (v0/*:: as any*/),
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "primaryEmail",
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
            "name": "emailVerifiedAt",
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
        "name": "currentSession",
        "plural": false,
        "selections": [
          (v0/*:: as any*/),
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "createdAt",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "expiresAt",
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
    "name": "AuthViewerQuery",
    "selections": (v1/*:: as any*/),
    "type": "Query",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": [],
    "kind": "Operation",
    "name": "AuthViewerQuery",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "06ed08d36d1ae7fe2bad6da27bb95623",
    "id": null,
    "metadata": {},
    "name": "AuthViewerQuery",
    "operationKind": "query",
    "text": "query AuthViewerQuery {\n  viewer {\n    user {\n      id\n      primaryEmail\n      status\n      emailVerifiedAt\n    }\n    currentSession {\n      id\n      createdAt\n      expiresAt\n      revokedAt\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "e2db45b327dd1ebefb6bd497b215a9f8";

export default node;
