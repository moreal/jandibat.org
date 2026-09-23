// Relay compiler source only. The page imports the generated artifact.
declare const graphql: (strings: TemplateStringsArray) => unknown;

export const exploreActivityQuery = graphql`
  query ExploreActivityQuery($handle: String!, $range: DateRangeInput!, $timezone: TimeZone!) {
    subject(handleOrID: $handle) {
      id
      handle
      activitySnapshot(range: $range, timezone: $timezone) {
        range { from to }
        generatedAt
        dataUpdatedAt
        revision
        days {
          date
          count
          level
          entries { environmentID action metricName metricValue metadata { key value } }
        }
        environments { id key name scope metadata { key value } }
      }
    }
  }
`;
