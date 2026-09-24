/**
 * @generated SignedSource<<3a1e77ae962becab32a2bf9651ad04d9>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type AuthBeginPasskeyRegistrationMutation$variables = Record<PropertyKey, never>;
export type AuthBeginPasskeyRegistrationMutation$data = {
  readonly beginPasskeyRegistration: {
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
export type AuthBeginPasskeyRegistrationMutation = {
  response: AuthBeginPasskeyRegistrationMutation$data;
  variables: AuthBeginPasskeyRegistrationMutation$variables;
};

const node: ConcreteRequest = (function(){
var v0 = [
  {
    "alias": null,
    "args": null,
    "concreteType": "BeginPasskeyRegistrationPayload",
    "kind": "LinkedField",
    "name": "beginPasskeyRegistration",
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
    "name": "AuthBeginPasskeyRegistrationMutation",
    "selections": (v0/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": [],
    "kind": "Operation",
    "name": "AuthBeginPasskeyRegistrationMutation",
    "selections": (v0/*:: as any*/)
  },
  "params": {
    "cacheID": "78fb7b8d06988dec44ed87bf70566d3c",
    "id": null,
    "metadata": {},
    "name": "AuthBeginPasskeyRegistrationMutation",
    "operationKind": "mutation",
    "text": "mutation AuthBeginPasskeyRegistrationMutation {\n  beginPasskeyRegistration {\n    errors {\n      code\n      message\n      field\n    }\n    options {\n      ceremonyID\n      publicKeyJSON\n      expiresAt\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "c248cac904fa6fbf47545abc9c167cca";

export default node;
