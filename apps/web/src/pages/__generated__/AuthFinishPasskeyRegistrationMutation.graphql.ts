/**
 * @generated SignedSource<<765b380ccc3e763392da2d66c3f9e071>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type FinishPasskeyRegistrationInput = {
  ceremonyID: string;
  credentialJSON: string;
  label?: string | null | undefined;
};
export type AuthFinishPasskeyRegistrationMutation$variables = {
  input: FinishPasskeyRegistrationInput;
};
export type AuthFinishPasskeyRegistrationMutation$data = {
  readonly finishPasskeyRegistration: {
    readonly credential: {
      readonly createdAt: any;
      readonly id: string;
      readonly label: string | null | undefined;
    } | null | undefined;
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
  };
};
export type AuthFinishPasskeyRegistrationMutation = {
  response: AuthFinishPasskeyRegistrationMutation$data;
  variables: AuthFinishPasskeyRegistrationMutation$variables;
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
    "concreteType": "FinishPasskeyRegistrationPayload",
    "kind": "LinkedField",
    "name": "finishPasskeyRegistration",
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
        "concreteType": "PasskeyCredential",
        "kind": "LinkedField",
        "name": "credential",
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
            "name": "label",
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
    ],
    "storageKey": null
  }
];
return {
  "fragment": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Fragment",
    "metadata": null,
    "name": "AuthFinishPasskeyRegistrationMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "AuthFinishPasskeyRegistrationMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "fa1cecfefa52f6ea9db46fb0d5fdaf09",
    "id": null,
    "metadata": {},
    "name": "AuthFinishPasskeyRegistrationMutation",
    "operationKind": "mutation",
    "text": "mutation AuthFinishPasskeyRegistrationMutation(\n  $input: FinishPasskeyRegistrationInput!\n) {\n  finishPasskeyRegistration(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    credential {\n      id\n      label\n      createdAt\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "65114beca8d367cb4c3262ab33545ded";

export default node;
