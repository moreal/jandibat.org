/**
 * @generated SignedSource<<4c266e0f7f1f9ab8ce3ce83ebcad4ae3>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type DateRangeInput = {
  from: any;
  to: any;
};
export type ExploreActivityQuery$variables = {
  handle: string;
  range: DateRangeInput;
  timezone: any;
};
export type ExploreActivityQuery$data = {
  readonly subject: {
    readonly activitySnapshot: {
      readonly dataUpdatedAt: any | null | undefined;
      readonly days: ReadonlyArray<{
        readonly count: any;
        readonly date: any;
        readonly entries: ReadonlyArray<{
          readonly action: string;
          readonly environmentID: string;
          readonly metadata: ReadonlyArray<{
            readonly key: string;
            readonly value: string;
          }>;
          readonly metricName: string;
          readonly metricValue: any;
        }>;
        readonly level: number;
      }>;
      readonly environments: ReadonlyArray<{
        readonly id: string;
        readonly key: string;
        readonly metadata: ReadonlyArray<{
          readonly key: string;
          readonly value: string;
        }>;
        readonly name: string;
        readonly scope: string;
      }>;
      readonly generatedAt: any;
      readonly range: {
        readonly from: any;
        readonly to: any;
      };
      readonly revision: string;
    };
    readonly handle: string;
    readonly id: string;
  } | null | undefined;
};
export type ExploreActivityQuery = {
  response: ExploreActivityQuery$data;
  variables: ExploreActivityQuery$variables;
};

const node: ConcreteRequest = (function(){
var v0 = [
  {
    "defaultValue": null,
    "kind": "LocalArgument",
    "name": "handle"
  },
  {
    "defaultValue": null,
    "kind": "LocalArgument",
    "name": "range"
  },
  {
    "defaultValue": null,
    "kind": "LocalArgument",
    "name": "timezone"
  }
],
v1 = {
  "alias": null,
  "args": null,
  "kind": "ScalarField",
  "name": "id",
  "storageKey": null
},
v2 = {
  "alias": null,
  "args": null,
  "kind": "ScalarField",
  "name": "key",
  "storageKey": null
},
v3 = {
  "alias": null,
  "args": null,
  "concreteType": "ActivityMetadataEntry",
  "kind": "LinkedField",
  "name": "metadata",
  "plural": true,
  "selections": [
    (v2/*:: as any*/),
    {
      "alias": null,
      "args": null,
      "kind": "ScalarField",
      "name": "value",
      "storageKey": null
    }
  ],
  "storageKey": null
},
v4 = [
  {
    "alias": null,
    "args": [
      {
        "kind": "Variable",
        "name": "handleOrID",
        "variableName": "handle"
      }
    ],
    "concreteType": "Subject",
    "kind": "LinkedField",
    "name": "subject",
    "plural": false,
    "selections": [
      (v1/*:: as any*/),
      {
        "alias": null,
        "args": null,
        "kind": "ScalarField",
        "name": "handle",
        "storageKey": null
      },
      {
        "alias": null,
        "args": [
          {
            "kind": "Variable",
            "name": "range",
            "variableName": "range"
          },
          {
            "kind": "Variable",
            "name": "timezone",
            "variableName": "timezone"
          }
        ],
        "concreteType": "ActivitySnapshot",
        "kind": "LinkedField",
        "name": "activitySnapshot",
        "plural": false,
        "selections": [
          {
            "alias": null,
            "args": null,
            "concreteType": "DateRange",
            "kind": "LinkedField",
            "name": "range",
            "plural": false,
            "selections": [
              {
                "alias": null,
                "args": null,
                "kind": "ScalarField",
                "name": "from",
                "storageKey": null
              },
              {
                "alias": null,
                "args": null,
                "kind": "ScalarField",
                "name": "to",
                "storageKey": null
              }
            ],
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "generatedAt",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "dataUpdatedAt",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "kind": "ScalarField",
            "name": "revision",
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "concreteType": "ActivityDay",
            "kind": "LinkedField",
            "name": "days",
            "plural": true,
            "selections": [
              {
                "alias": null,
                "args": null,
                "kind": "ScalarField",
                "name": "date",
                "storageKey": null
              },
              {
                "alias": null,
                "args": null,
                "kind": "ScalarField",
                "name": "count",
                "storageKey": null
              },
              {
                "alias": null,
                "args": null,
                "kind": "ScalarField",
                "name": "level",
                "storageKey": null
              },
              {
                "alias": null,
                "args": null,
                "concreteType": "ActivityEntry",
                "kind": "LinkedField",
                "name": "entries",
                "plural": true,
                "selections": [
                  {
                    "alias": null,
                    "args": null,
                    "kind": "ScalarField",
                    "name": "environmentID",
                    "storageKey": null
                  },
                  {
                    "alias": null,
                    "args": null,
                    "kind": "ScalarField",
                    "name": "action",
                    "storageKey": null
                  },
                  {
                    "alias": null,
                    "args": null,
                    "kind": "ScalarField",
                    "name": "metricName",
                    "storageKey": null
                  },
                  {
                    "alias": null,
                    "args": null,
                    "kind": "ScalarField",
                    "name": "metricValue",
                    "storageKey": null
                  },
                  (v3/*:: as any*/)
                ],
                "storageKey": null
              }
            ],
            "storageKey": null
          },
          {
            "alias": null,
            "args": null,
            "concreteType": "ActivityEnvironment",
            "kind": "LinkedField",
            "name": "environments",
            "plural": true,
            "selections": [
              (v1/*:: as any*/),
              (v2/*:: as any*/),
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
                "name": "scope",
                "storageKey": null
              },
              (v3/*:: as any*/)
            ],
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
    "name": "ExploreActivityQuery",
    "selections": (v4/*:: as any*/),
    "type": "Query",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*:: as any*/),
    "kind": "Operation",
    "name": "ExploreActivityQuery",
    "selections": (v4/*:: as any*/)
  },
  "params": {
    "cacheID": "715d5f8d0254a1cea280c0810a6f806e",
    "id": null,
    "metadata": {},
    "name": "ExploreActivityQuery",
    "operationKind": "query",
    "text": "query ExploreActivityQuery(\n  $handle: String!\n  $range: DateRangeInput!\n  $timezone: TimeZone!\n) {\n  subject(handleOrID: $handle) {\n    id\n    handle\n    activitySnapshot(range: $range, timezone: $timezone) {\n      range {\n        from\n        to\n      }\n      generatedAt\n      dataUpdatedAt\n      revision\n      days {\n        date\n        count\n        level\n        entries {\n          environmentID\n          action\n          metricName\n          metricValue\n          metadata {\n            key\n            value\n          }\n        }\n      }\n      environments {\n        id\n        key\n        name\n        scope\n        metadata {\n          key\n          value\n        }\n      }\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "8e70220036979b4b5a07395be53bf0ed";

export default node;
