/**
 * @generated SignedSource<<c71f1c6d2356e8aabeb1759246c4189c>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type RequestSubjectDeletionInput = {
  subjectID: string;
};
export type AuthRequestSubjectDeletionMutation$variables = {
  input: RequestSubjectDeletionInput;
};
export type AuthRequestSubjectDeletionMutation$data = {
  readonly requestSubjectDeletion: {
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
    readonly request: {
      readonly requestID: string;
      readonly status: string;
    } | null | undefined;
  };
};
export type AuthRequestSubjectDeletionMutation = {
  response: AuthRequestSubjectDeletionMutation$data;
  variables: AuthRequestSubjectDeletionMutation$variables;
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
    "concreteType": "RequestSubjectDeletionPayload",
    "kind": "LinkedField",
    "name": "requestSubjectDeletion",
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
        "concreteType": "DeletionRequestResult",
        "kind": "LinkedField",
        "name": "request",
        "plural": false,
        "selections": [
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "requestID",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "status",
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
    "name": "AuthRequestSubjectDeletionMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "AuthRequestSubjectDeletionMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "aa8aeb8704b3a56cd39e824e8d400bc4",
    "id": null,
    "metadata": {},
    "name": "AuthRequestSubjectDeletionMutation",
    "operationKind": "mutation",
    "text": "mutation AuthRequestSubjectDeletionMutation(\n  $input: RequestSubjectDeletionInput!\n) {\n  requestSubjectDeletion(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    request {\n      requestID\n      status\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "fc2dd334cdb37c6fa9d18b67b6a0ffb2";

export default node;
