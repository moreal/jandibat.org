/**
 * @generated SignedSource<<94720eda9cd3b0b81d4b826cb0ccc397>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
import { FragmentRefs } from "relay-runtime";
export type RelayCompatQuery$variables = {
  id: string;
};
export type RelayCompatQuery$data = {
  readonly subject: {
    readonly " $fragmentSpreads": FragmentRefs<"RelayCompatSubjectFragment">;
  } | null | undefined;
};
export type RelayCompatQuery = {
  response: RelayCompatQuery$data;
  variables: RelayCompatQuery$variables;
};

const node: ConcreteRequest = (function(){
var v0 = [
  {
    "defaultValue": null,
    "kind": "LocalArgument",
    "name": "id"
  }
],
v1 = [
  {
    "kind": "Variable",
    "name": "handleOrID",
    "variableName": "id"
  }
];
return {
  "fragment": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Fragment",
    "metadata": null,
    "name": "RelayCompatQuery",
    "selections": [
      {
        "alias": null,
        "args": (v1/*:: as any*/),
        "concreteType": "Subject",
        "kind": "LinkedField",
        "name": "subject",
        "plural": false,
        "selections": [
          {
            "args": null,
            "kind": "FragmentSpread",
            "name": "RelayCompatSubjectFragment"
          }
        ],
        "storageKey": null
      }
    ],
    "type": "Query",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "RelayCompatQuery",
    "selections": [
      {
        "alias": null,
        "args": (v1/*:: as any*/),
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
    ]
  },
  "params": {
    "cacheID": "2c57ef7bf5afc15b49106cbbf43f35f3",
    "id": null,
    "metadata": {},
    "name": "RelayCompatQuery",
    "operationKind": "query",
    "text": "query RelayCompatQuery(\n  $id: String!\n) {\n  subject(handleOrID: $id) {\n    ...RelayCompatSubjectFragment\n    id\n  }\n}\n\nfragment RelayCompatSubjectFragment on Subject {\n  id\n  displayName\n}\n"
  }
};
})();

(node as any).hash = "7b84d2e7f815d5de071d0460685e4b12";

export default node;
