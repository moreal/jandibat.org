package observability

import (
	"strings"
	"testing"
	"time"
)

func TestObserveGraphQLRecordsBoundedOperationAndDuration(t *testing.T) {
	registry := NewRegistry(Resource{Environment: "test"})
	registry.ObserveGraphQL("query", "GraphQLContractQuery", "succeeded", 75*time.Millisecond)
	registry.ObserveGraphQL("mutation", "GraphQLContractQuery", "failed", -time.Second)

	body := scrape(t, registry)
	for _, fragment := range []string{
		`graphql_operations_total{operation_type="query",operation_name="GraphQLContractQuery",outcome="succeeded",build_sha="unknown",environment="test",region="unknown"} 1`,
		`graphql_operation_duration_seconds_bucket{operation_type="query",operation_name="GraphQLContractQuery",le="0.1",build_sha="unknown",environment="test",region="unknown"} 1`,
		`graphql_operations_total{operation_type="mutation",operation_name="GraphQLContractQuery",outcome="failed",build_sha="unknown",environment="test",region="unknown"} 1`,
		`graphql_operation_duration_seconds_sum{operation_type="mutation",operation_name="GraphQLContractQuery",build_sha="unknown",environment="test",region="unknown"} 0`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("missing GraphQL metric %q:\n%s", fragment, body)
		}
	}
}

func TestObserveGraphQLNeverExposesClientControlledLabels(t *testing.T) {
	registry := NewRegistry(Resource{})
	secret := "Bearer-client-chosen-operation-name"
	registry.ObserveGraphQL("query", secret, "succeeded", time.Millisecond)
	registry.ObserveGraphQL("unexpected-type", secret, secret, time.Millisecond)
	registry.ObserveGraphQL("query", "", "rejected", time.Millisecond)

	body := scrape(t, registry)
	for _, fragment := range []string{
		`graphql_operations_total{operation_type="query",operation_name="other",outcome="succeeded"`,
		`graphql_operations_total{operation_type="other",operation_name="other",outcome="unknown"`,
		`graphql_operations_total{operation_type="query",operation_name="other",outcome="rejected"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("missing bounded GraphQL metric %q:\n%s", fragment, body)
		}
	}
	if strings.Contains(body, secret) || strings.Contains(body, "unexpected-type") {
		t.Fatal("client-controlled GraphQL label reached Prometheus output")
	}
}
