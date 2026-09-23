/**
 * @generated SignedSource<<6a1e568b35d708e931f224187676d730>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ReaderFragment } from 'relay-runtime';
import { FragmentRefs } from "relay-runtime";
export type RelayCrossRouteSubjectFragment$data = {
  readonly displayName: string | null | undefined;
  readonly id: string;
  readonly " $fragmentType": "RelayCrossRouteSubjectFragment";
};
export type RelayCrossRouteSubjectFragment$key = {
  readonly " $data"?: RelayCrossRouteSubjectFragment$data;
  readonly " $fragmentSpreads": FragmentRefs<"RelayCrossRouteSubjectFragment">;
};

const node: ReaderFragment = {
  "argumentDefinitions": [],
  "kind": "Fragment",
  "metadata": null,
  "name": "RelayCrossRouteSubjectFragment",
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
  "type": "Subject",
  "abstractKey": null
};

(node as any).hash = "4945dcce5474bc9c761f1138090077d4";

export default node;
