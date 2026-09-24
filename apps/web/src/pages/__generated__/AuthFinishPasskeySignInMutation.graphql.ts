/**
 * @generated SignedSource<<d1376785652c9948e7121ed268192f31>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type FinishPasskeySignInInput = {
  ceremonyID: string;
  credentialJSON: string;
};
export type AuthFinishPasskeySignInMutation$variables = {
  input: FinishPasskeySignInInput;
};
export type AuthFinishPasskeySignInMutation$data = {
  readonly finishPasskeySignIn: {
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly session: {
      readonly expiresAt: any;
      readonly id: string;
    } | null | undefined;
  };
};
export type AuthFinishPasskeySignInMutation = {
  response: AuthFinishPasskeySignInMutation$data;
  variables: AuthFinishPasskeySignInMutation$variables;
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
    "concreteType": "FinishPasskeySignInPayload",
    "kind": "LinkedField",
    "name": "finishPasskeySignIn",
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
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Fragment",
    "metadata": null,
    "name": "AuthFinishPasskeySignInMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "AuthFinishPasskeySignInMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "27ef56a5636749be813674c5eab501f4",
    "id": null,
    "metadata": {},
    "name": "AuthFinishPasskeySignInMutation",
    "operationKind": "mutation",
    "text": "mutation AuthFinishPasskeySignInMutation(\n  $input: FinishPasskeySignInInput!\n) {\n  finishPasskeySignIn(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    session {\n      id\n      expiresAt\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "c0d89c1a564d89192afa9a206014b66a";

export default node;
