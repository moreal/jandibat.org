/**
 * @generated SignedSource<<471e2f079f9bbd1afc4358611e321b57>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
import { FragmentRefs } from "relay-runtime";
export type RelayCrossRouteSubjectQuery$variables = {
  id: string;
};
export type RelayCrossRouteSubjectQuery$data = {
  readonly subject: {
    readonly " $fragmentSpreads": FragmentRefs<"RelayCrossRouteSubjectFragment">;
  } | null | undefined;
};
export type RelayCrossRouteSubjectQuery = {
  response: RelayCrossRouteSubjectQuery$data;
  variables: RelayCrossRouteSubjectQuery$variables;
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
    "name": "RelayCrossRouteSubjectQuery",
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
            "name": "RelayCrossRouteSubjectFragment"
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
    "name": "RelayCrossRouteSubjectQuery",
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
    "cacheID": "be2664b97f973fc4a5195ce67b3289b6",
    "id": null,
    "metadata": {},
    "name": "RelayCrossRouteSubjectQuery",
    "operationKind": "query",
    "text": "query RelayCrossRouteSubjectQuery(\n  $id: String!\n) {\n  subject(handleOrID: $id) {\n    ...RelayCrossRouteSubjectFragment\n    id\n  }\n}\n\nfragment RelayCrossRouteSubjectFragment on Subject {\n  id\n  displayName\n}\n"
  }
};
})();

(node as any).hash = "717f157ee483992bf242e6a807b6b24b";

export default node;
