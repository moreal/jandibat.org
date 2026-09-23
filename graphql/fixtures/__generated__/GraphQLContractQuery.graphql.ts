/**
 * @generated SignedSource<<22086216066cc55a6659730a24c209b3>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type GraphQLContractQuery$variables = Record<PropertyKey, never>;
export type GraphQLContractQuery$data = {
  readonly _contract: boolean;
};
export type GraphQLContractQuery = {
  response: GraphQLContractQuery$data;
  variables: GraphQLContractQuery$variables;
};

const node: ConcreteRequest = (function(){
var v0 = [
  {
    "alias": null,
    "args": null,
    "kind": "ScalarField",
    "name": "_contract",
    "storageKey": null
  }
];
return {
  "fragment": {
    "argumentDefinitions": [],
    "kind": "Fragment",
    "metadata": null,
    "name": "GraphQLContractQuery",
    "selections": (v0/*:: as any*/),
    "type": "Query",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": [],
    "kind": "Operation",
    "name": "GraphQLContractQuery",
    "selections": (v0/*:: as any*/)
  },
  "params": {
    "cacheID": "261df61bedc6545dadd7ea419651495a",
    "id": null,
    "metadata": {},
    "name": "GraphQLContractQuery",
    "operationKind": "query",
    "text": "query GraphQLContractQuery {\n  _contract\n}\n"
  }
};
})();

(node as any).hash = "b3016d32a2022095bce45bd09e0f34f7";

export default node;
