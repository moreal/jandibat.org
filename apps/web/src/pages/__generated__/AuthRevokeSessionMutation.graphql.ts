/**
 * @generated SignedSource<<aef97a92c3299f4abe77617645423204>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type RevokeSessionInput = {
  id: string;
};
export type AuthRevokeSessionMutation$variables = {
  input: RevokeSessionInput;
};
export type AuthRevokeSessionMutation$data = {
  readonly revokeSession: {
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
export type AuthRevokeSessionMutation = {
  response: AuthRevokeSessionMutation$data;
  variables: AuthRevokeSessionMutation$variables;
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
    "concreteType": "RevokeSessionPayload",
    "kind": "LinkedField",
    "name": "revokeSession",
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
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Fragment",
    "metadata": null,
    "name": "AuthRevokeSessionMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "AuthRevokeSessionMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "60e60f6888eb84938af42e82a1657863",
    "id": null,
    "metadata": {},
    "name": "AuthRevokeSessionMutation",
    "operationKind": "mutation",
    "text": "mutation AuthRevokeSessionMutation(\n  $input: RevokeSessionInput!\n) {\n  revokeSession(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    session {\n      id\n      revokedAt\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "19ec6977d6e9402d3523a91e2b290bb9";

export default node;
