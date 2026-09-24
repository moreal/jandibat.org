/**
 * @generated SignedSource<<cd43eb10014a10871d2e2d3ef3b66d12>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type RequestMagicLinkInput = {
  email: string;
  redirectURI?: string | null | undefined;
};
export type AuthRequestMagicLinkMutation$variables = {
  input: RequestMagicLinkInput;
};
export type AuthRequestMagicLinkMutation$data = {
  readonly requestMagicLink: {
    readonly accepted: boolean;
    readonly errors: ReadonlyArray<{
      readonly code: string;
      readonly field: string | null | undefined;
      readonly message: string;
    }>;
  };
};
export type AuthRequestMagicLinkMutation = {
  response: AuthRequestMagicLinkMutation$data;
  variables: AuthRequestMagicLinkMutation$variables;
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
    "concreteType": "RequestMagicLinkPayload",
    "kind": "LinkedField",
    "name": "requestMagicLink",
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
        "name": "accepted",
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
    "name": "AuthRequestMagicLinkMutation",
    "selections": (v1/*:: as any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "AuthRequestMagicLinkMutation",
    "selections": (v1/*:: as any*/)
  },
  "params": {
    "cacheID": "63f4df4186a914abfca51aaf2442b809",
    "id": null,
    "metadata": {},
    "name": "AuthRequestMagicLinkMutation",
    "operationKind": "mutation",
    "text": "mutation AuthRequestMagicLinkMutation(\n  $input: RequestMagicLinkInput!\n) {\n  requestMagicLink(input: $input) {\n    errors {\n      code\n      message\n      field\n    }\n    accepted\n  }\n}\n"
  }
};
})();

(node as any).hash = "0352bc16586929ea8d21f4076445bce5";

export default node;
