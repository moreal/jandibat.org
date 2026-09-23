/**
 * @generated SignedSource<<1c9b0cefe3d6d0ac6f71753773a52c95>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type UpdateSubjectInput = {
  clearDisplayName?: boolean | null | undefined;
  displayName?: string | null | undefined;
  handle?: string | null | undefined;
  id: string;
};
export type RelayCompatUpdateSubjectMutation$variables = {
  input: UpdateSubjectInput;
};
export type RelayCompatUpdateSubjectMutation$data = {
  readonly updateSubject: {
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly subject: {
      readonly displayName: string | null | undefined;
      readonly id: string;
    } | null | undefined;
  };
};
export type RelayCompatUpdateSubjectMutation = {
  response: RelayCompatUpdateSubjectMutation$data;
  variables: RelayCompatUpdateSubjectMutation$variables;
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
    "concreteType": "UpdateSubjectPayload",
    "kind": "LinkedField",
    "name": "updateSubject",
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
            "name": "field",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "message",
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
            "name": "displayName",
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
    "name": "RelayCompatUpdateSubjectMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "RelayCompatUpdateSubjectMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "b31c181a39cbdc6d1c1c24e9512c36b8",
    "id": null,
    "metadata": {},
    "name": "RelayCompatUpdateSubjectMutation",
    "operationKind": "mutation",
    "text": "mutation RelayCompatUpdateSubjectMutation(\n  $input: UpdateSubjectInput!\n) {\n  updateSubject(input: $input) {\n    errors {\n      code\n      field\n      message\n    }\n    subject {\n      id\n      displayName\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "72871695b69b412a11a28dd080985b36";

export default node;
