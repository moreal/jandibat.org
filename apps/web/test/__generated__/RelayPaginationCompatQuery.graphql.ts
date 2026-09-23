/**
 * @generated SignedSource<<01fa5ccc0ca93bfd98fa6a60c025a833>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
import { FragmentRefs } from "relay-runtime";
export type RelayPaginationCompatQuery$variables = {
  count: number;
  cursor?: any | null | undefined;
  id: string;
};
export type RelayPaginationCompatQuery$data = {
  readonly subject: {
    readonly " $fragmentSpreads": FragmentRefs<"RelayPaginationSubjectFragment">;
  } | null | undefined;
};
export type RelayPaginationCompatQuery = {
  response: RelayPaginationCompatQuery$data;
  variables: RelayPaginationCompatQuery$variables;
};

const node: ConcreteRequest = (function(){
var v0 = {
  "defaultValue": null,
  "kind": "LocalArgument",
  "name": "count"
},
v1 = {
  "defaultValue": null,
  "kind": "LocalArgument",
  "name": "cursor"
},
v2 = {
  "defaultValue": null,
  "kind": "LocalArgument",
  "name": "id"
},
v3 = [
  {
    "kind": "Variable",
    "name": "handleOrID",
    "variableName": "id"
  }
],
v4 = {
  "alias": null,
  "args": null,
  "kind": "ScalarField",
  "name": "id",
  "storageKey": null
},
v5 = [
  {
    "kind": "Variable",
    "name": "after",
    "variableName": "cursor"
  },
  {
    "kind": "Variable",
    "name": "first",
    "variableName": "count"
  }
];
return {
  "fragment": {
    "argumentDefinitions": [
      (v0/*:: as any*/),
      (v1/*:: as any*/),
      (v2/*:: as any*/)
    ],
    "kind": "Fragment",
    "metadata": null,
    "name": "RelayPaginationCompatQuery",
    "selections": [
      {
        "alias": null,
        "args": (v3/*:: as any*/),
        "concreteType": "Subject",
        "kind": "LinkedField",
        "name": "subject",
        "plural": false,
        "selections": [
          {
            "args": [
              {
                "kind": "Variable",
                "name": "count",
                "variableName": "count"
              },
              {
                "kind": "Variable",
                "name": "cursor",
                "variableName": "cursor"
              }
            ],
            "kind": "FragmentSpread",
            "name": "RelayPaginationSubjectFragment"
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
    "argumentDefinitions": [
      (v2/*:: as any*/),
      (v0/*:: as any*/),
      (v1/*:: as any*/)
    ],
    "kind": "Operation",
    "name": "RelayPaginationCompatQuery",
    "selections": [
      {
        "alias": null,
        "args": (v3/*:: as any*/),
        "concreteType": "Subject",
        "kind": "LinkedField",
        "name": "subject",
        "plural": false,
        "selections": [
          (v4/*:: as any*/),
          {
            "alias": null,
            "args": (v5/*:: as any*/),
            "concreteType": "ProviderConnectionConnection",
            "kind": "LinkedField",
            "name": "providerConnections",
            "plural": false,
            "selections": [
              {
                "alias": null,
                "args": null,
                "concreteType": "ProviderConnectionEdge",
                "kind": "LinkedField",
                "name": "edges",
                "plural": true,
                "selections": [
                  {
                    "alias": null,
                    "args": null,
                    "concreteType": "ProviderConnection",
                    "kind": "LinkedField",
                    "name": "node",
                    "plural": false,
                    "selections": [
                      (v4/*:: as any*/),
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
                        "name": "__typename",
                        "storageKey": null
                      }
                    ],
                    "storageKey": null
                  },
                  {
                    "alias": null,
                    "args": null,
                    "kind": "ScalarField",
                    "name": "cursor",
                    "storageKey": null
                  }
                ],
                "storageKey": null
              },
              {
                "alias": null,
                "args": null,
                "concreteType": "PageInfo",
                "kind": "LinkedField",
                "name": "pageInfo",
                "plural": false,
                "selections": [
                  {
                    "alias": null,
                    "args": null,
                    "kind": "ScalarField",
                    "name": "endCursor",
                    "storageKey": null
                  },
                  {
                    "alias": null,
                    "args": null,
                    "kind": "ScalarField",
                    "name": "hasNextPage",
                    "storageKey": null
                  }
                ],
                "storageKey": null
              }
            ],
            "storageKey": null
          },
          {
            "alias": null,
            "args": (v5/*:: as any*/),
            "filters": null,
            "handle": "connection",
            "key": "RelayPaginationSubject_providerConnections",
            "kind": "LinkedHandle",
            "name": "providerConnections"
          }
        ],
        "storageKey": null
      }
    ]
  },
  "params": {
    "cacheID": "7a8fee382dbe2cb8ffd3e35bfe24e9c5",
    "id": null,
    "metadata": {},
    "name": "RelayPaginationCompatQuery",
    "operationKind": "query",
    "text": "query RelayPaginationCompatQuery(\n  $id: String!\n  $count: Int!\n  $cursor: Cursor\n) {\n  subject(handleOrID: $id) {\n    ...RelayPaginationSubjectFragment_1G22uz\n    id\n  }\n}\n\nfragment RelayPaginationSubjectFragment_1G22uz on Subject {\n  id\n  providerConnections(first: $count, after: $cursor) {\n    edges {\n      node {\n        id\n        status\n        __typename\n      }\n      cursor\n    }\n    pageInfo {\n      endCursor\n      hasNextPage\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "814bff40907b43780253eac4a1f0e9a2";

export default node;
