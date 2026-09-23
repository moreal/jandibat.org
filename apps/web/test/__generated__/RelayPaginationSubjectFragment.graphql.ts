/**
 * @generated SignedSource<<d4e9a36dc1b4373936fb635d1358974e>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ReaderFragment } from 'relay-runtime';
import { FragmentRefs } from "relay-runtime";
export type RelayPaginationSubjectFragment$data = {
  readonly id: string;
  readonly providerConnections: {
    readonly edges: ReadonlyArray<{
      readonly node: {
        readonly id: string;
        readonly status: string;
      };
    }>;
  } | null | undefined;
  readonly " $fragmentType": "RelayPaginationSubjectFragment";
};
export type RelayPaginationSubjectFragment$key = {
  readonly " $data"?: RelayPaginationSubjectFragment$data;
  readonly " $fragmentSpreads": FragmentRefs<"RelayPaginationSubjectFragment">;
};

import RelayPaginationSubjectRefetchQuery_graphql from './RelayPaginationSubjectRefetchQuery.graphql';

const node: ReaderFragment = (function(){
var v0 = [
  "providerConnections"
],
v1 = {
  "alias": null,
  "args": null,
  "kind": "ScalarField",
  "name": "id",
  "storageKey": null
};
return {
  "argumentDefinitions": [
    {
      "defaultValue": null,
      "kind": "LocalArgument",
      "name": "count"
    },
    {
      "defaultValue": null,
      "kind": "LocalArgument",
      "name": "cursor"
    }
  ],
  "kind": "Fragment",
  "metadata": {
    "connection": [
      {
        "count": "count",
        "cursor": "cursor",
        "direction": "forward",
        "path": (v0/*:: as any*/)
      }
    ],
    "refetch": {
      "connection": {
        "forward": {
          "count": "count",
          "cursor": "cursor"
        },
        "backward": null,
        "path": (v0/*:: as any*/)
      },
      "fragmentPathInResult": [
        "node"
      ],
      "operation": RelayPaginationSubjectRefetchQuery_graphql,
      "identifierInfo": {
        "identifierField": "id",
        "identifierQueryVariableName": "id"
      }
    }
  },
  "name": "RelayPaginationSubjectFragment",
  "selections": [
    (v1/*:: as any*/),
    {
      "alias": "providerConnections",
      "args": null,
      "concreteType": "ProviderConnectionConnection",
      "kind": "LinkedField",
      "name": "__RelayPaginationSubject_providerConnections_connection",
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
                (v1/*:: as any*/),
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
    }
  ],
  "type": "Subject",
  "abstractKey": null
};
})();

(node as any).hash = "d748fef6e3edbcd881d11b138864783b";

export default node;
