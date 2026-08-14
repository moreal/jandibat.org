import { defineFileRoute } from "@solidjs/router/fs";
import type { RouteProps } from "@solidjs/router";
import { ExplorePage } from "../../pages/ExplorePage";

export const route = defineFileRoute("/explore/:subject?", {});

export default function SubjectExploreRoute(props: RouteProps<typeof route>) {
  return <ExplorePage subject={props.params.subject} />;
}
