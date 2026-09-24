/**
 * @generated SignedSource<<57abca08b34e25240d2138e58b4371be>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type CreateSubjectInput = {
  displayName?: string | null | undefined;
  handle: string;
  isPublic?: boolean | null | undefined;
  timezone: any;
};
export type AuthCreateSubjectMutation$variables = {
  input: CreateSubjectInput;
};
export type AuthCreateSubjectMutation$data = {
  readonly createSubject: {
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly subject: {
      readonly createdAt: any;
      readonly displayName: string | null | undefined;
      readonly handle: string;
      readonly id: string;
      readonly isPublic: boolean;
      readonly timezone: any;
      readonly updatedAt: any;
    } | null | undefined;
  };
};
export type AuthCreateSubjectMutation = {
  response: AuthCreateSubjectMutation$data;
  variables: AuthCreateSubjectMutation$variables;
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
    "concreteType": "CreateSubjectPayload",
    "kind": "LinkedField",
    "name": "createSubject",
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
        "concreteType": "Subject",
        "kind": "LinkedField",
        "name": "subject",
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
            "name": "handle",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "displayName",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "timezone",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "isPublic",
            "storageKey": null
          },
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
    "name": "AuthCreateSubjectMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "AuthCreateSubjectMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "ec6dfb5af86dd56aa02b7a70cc43a14c",
    "id": null,
    "metadata": {},
    "name": "AuthCreateSubjectMutation",
    "operationKind": "mutation",
    "text": "mutation AuthCreateSubjectMutation(\n  $input: CreateSubjectInput!\n) {\n  createSubject(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    subject {\n      id\n      handle\n      displayName\n      timezone\n      isPublic\n      createdAt\n      updatedAt\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "d6d10063332e2dbcc67c87857f977cc9";

export default node;
