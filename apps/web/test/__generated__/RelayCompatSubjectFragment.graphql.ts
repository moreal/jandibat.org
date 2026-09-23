/**
 * @generated SignedSource<<9f27b850de1cb9568215c5c3c9492ee7>>
 * @lightSyntaxTransform
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ReaderFragment } from 'relay-runtime';
import { FragmentRefs } from "relay-runtime";
export type RelayCompatSubjectFragment$data = {
  readonly displayName: string | null | undefined;
  readonly id: string;
  readonly " $fragmentType": "RelayCompatSubjectFragment";
};
export type RelayCompatSubjectFragment$key = {
  readonly " $data"?: RelayCompatSubjectFragment$data;
  readonly " $fragmentSpreads": FragmentRefs<"RelayCompatSubjectFragment">;
};

const node: ReaderFragment = {
  "argumentDefinitions": [],
  "kind": "Fragment",
  "metadata": null,
  "name": "RelayCompatSubjectFragment",
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

(node as any).hash = "af7b4f440bbd38519ac04eafd914c13b";

export default node;
