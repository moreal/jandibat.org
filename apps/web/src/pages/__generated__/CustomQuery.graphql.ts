/**
 * @generated SignedSource<<16a33bbfb726f2b6f70ee917010db2d3>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
import { FragmentRefs } from "relay-runtime";
export type CustomQuery$variables = {
  count: number;
  cursor?: any | null | undefined;
  subject: string;
};
export type CustomQuery$data = {
  readonly subject: {
    readonly id: string;
    readonly " $fragmentSpreads": FragmentRefs<"CustomSubjectFragment">;
  } | null | undefined;
};
export type CustomQuery = {
  response: CustomQuery$data;
  variables: CustomQuery$variables;
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
  "name": "subject"
},
v3 = [
  {
    "kind": "Variable",
    "name": "handleOrID",
    "variableName": "subject"
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
    "name": "CustomQuery",
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
            "name": "CustomSubjectFragment"
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
    "name": "CustomQuery",
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
            "concreteType": "CustomProviderConnection",
            "kind": "LinkedField",
            "name": "customProviders",
            "plural": false,
            "selections": [
              {
                "alias": null,
                "args": null,
                "concreteType": "CustomProviderEdge",
                "kind": "LinkedField",
                "name": "edges",
                "plural": true,
                "selections": [
                  {
                    "alias": null,
                    "args": null,
                    "concreteType": "CustomProvider",
                    "kind": "LinkedField",
                    "name": "node",
                    "plural": false,
                    "selections": [
                      (v4/*:: as any*/),
                      {
                        "alias": null,
                        "args": null,
                        "kind": "ScalarField",
                        "name": "ingestProviderID",
                        "storageKey": null
                      },
                      {
                        "alias": null,
                        "args": null,
                        "kind": "ScalarField",
                        "name": "slug",
                        "storageKey": null
                      },
                      {
                        "alias": null,
                        "args": null,
                        "kind": "ScalarField",
                        "name": "name",
                        "storageKey": null
                      },
                      {
                        "alias": null,
                        "args": null,
                        "kind": "ScalarField",
                        "name": "description",
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
                        "name": "allowedActions",
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
                    "name": "hasNextPage",
                    "storageKey": null
                  },
                  {
                    "alias": null,
                    "args": null,
                    "kind": "ScalarField",
                    "name": "endCursor",
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
            "key": "CustomSubject_customProviders",
            "kind": "LinkedHandle",
            "name": "customProviders"
          }
        ],
        "storageKey": null
      }
    ]
  },
  "params": {
    "cacheID": "2c0c3150b81863c82d51b5203398c8ae",
    "id": null,
    "metadata": {},
    "name": "CustomQuery",
    "operationKind": "query",
    "text": "query CustomQuery(\n  $subject: String!\n  $count: Int!\n  $cursor: Cursor\n) {\n  subject(handleOrID: $subject) {\n    id\n    ...CustomSubjectFragment_1G22uz\n  }\n}\n\nfragment CustomSubjectFragment_1G22uz on Subject {\n  id\n  customProviders(first: $count, after: $cursor) {\n    edges {\n      node {\n        id\n        ingestProviderID\n        slug\n        name\n        description\n        status\n        allowedActions\n        createdAt\n        updatedAt\n        __typename\n      }\n      cursor\n    }\n    pageInfo {\n      hasNextPage\n      endCursor\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "5825eabd439ef7e7e73698e68cecf634";

export default node;
