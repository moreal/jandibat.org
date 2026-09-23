/**
 * @generated SignedSource<<6dc42aa99919e3cd02ebf911c003debf>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type ProviderAuthMethod = "OAUTH2" | "PUBLIC" | "TOKEN" | "%future added value";
export type ConnectProviderInput = {
  authMethod: ProviderAuthMethod;
  includePrivate?: boolean | null | undefined;
  providerID: string;
  redirectURI?: string | null | undefined;
  scopes?: ReadonlyArray<string> | null | undefined;
  subjectID: string;
  token?: string | null | undefined;
};
export type ConnectionsConnectProviderMutation$variables = {
  input: ConnectProviderInput;
};
export type ConnectionsConnectProviderMutation$data = {
  readonly connectProvider: {
    readonly authorizationURL: string | null | undefined;
    readonly connection: {
      readonly authMethod: string;
      readonly id: string;
      readonly lastSyncedAt: any | null | undefined;
      readonly privateDataEnabled: boolean;
      readonly providerID: string;
      readonly status: string;
    } | null | undefined;
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
  };
};
export type ConnectionsConnectProviderMutation = {
  response: ConnectionsConnectProviderMutation$data;
  variables: ConnectionsConnectProviderMutation$variables;
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
    "concreteType": "ConnectProviderPayload",
    "kind": "LinkedField",
    "name": "connectProvider",
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
        "name": "authorizationURL",
        "storageKey": null
      },
      {
        "alias": null,
        "args": null,
        "concreteType": "ProviderConnection",
        "kind": "LinkedField",
        "name": "connection",
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
            "name": "providerID",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "authMethod",
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
            "name": "privateDataEnabled",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "lastSyncedAt",
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
    "name": "ConnectionsConnectProviderMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "ConnectionsConnectProviderMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "855028dd01de2cbeb015dcfd158a9181",
    "id": null,
    "metadata": {},
    "name": "ConnectionsConnectProviderMutation",
    "operationKind": "mutation",
    "text": "mutation ConnectionsConnectProviderMutation(\n  $input: ConnectProviderInput!\n) {\n  connectProvider(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    authorizationURL\n    connection {\n      id\n      providerID\n      authMethod\n      status\n      privateDataEnabled\n      lastSyncedAt\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "6b1ff2b4b333d49c88803322320a5c1d";

export default node;
