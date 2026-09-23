package apihttp

import (
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	graph "github.com/moreal/jandibat.org/apps/api/internal/graphql"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/vektah/gqlparser/v2/ast"
	"go.uber.org/zap"
)

const maxGraphQLObservedResponse = 64 << 10

func graphQLOperationObserver(next stdhttp.Handler, registry *observability.Registry, logger *zap.Logger) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path != "/graphql" {
			next.ServeHTTP(w, r)
			return
		}
		metadata, ok := graph.OperationMetadataFromContext(r.Context())
		if !ok {
			// The preflight is mandatory for GraphQL. Never create an unbounded
			// metrics/logging label from a request that bypassed it.
			writeFrameworkProblem(w, r, stdhttp.StatusServiceUnavailable, "Service Unavailable", "service_unavailable", "The operation could not be verified.")
			return
		}
		started := time.Now()
		captured := &graphQLObservedResponseWriter{destination: w}
		next.ServeHTTP(captured, r)
		elapsed := time.Since(started)
		outcome := graphQLOutcome(r.Context(), metadata.Type, captured.status, captured.body.Bytes(), captured.truncated)
		operationType := string(metadata.Type)
		if metadata.Type != ast.Query && metadata.Type != ast.Mutation {
			operationType = "other"
		}
		operationName := "other"
		if metadata.Name == "GraphQLContractQuery" {
			operationName = metadata.Name
		}
		observability.Log(logger, "graphql.operation_completed",
			observability.SafeString("request_id", middleware.GetReqID(r.Context())),
			zap.String("operation_type", operationType),
			zap.String("operation_name", operationName),
			zap.String("outcome", outcome),
			zap.Duration("duration", elapsed))
		registry.ObserveGraphQL(operationType, operationName, outcome, elapsed)
	})
}

type graphQLObservedResponseWriter struct {
	destination stdhttp.ResponseWriter
	status      int
	body        bytes.Buffer
	truncated   bool
}

func (writer *graphQLObservedResponseWriter) Header() stdhttp.Header {
	return writer.destination.Header()
}
func (writer *graphQLObservedResponseWriter) Unwrap() stdhttp.ResponseWriter {
	return writer.destination
}
func (writer *graphQLObservedResponseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
		writer.destination.WriteHeader(status)
	}
}
func (writer *graphQLObservedResponseWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(stdhttp.StatusOK)
	}
	remaining := maxGraphQLObservedResponse - writer.body.Len()
	if remaining < len(value) {
		writer.truncated = true
	}
	if remaining > 0 {
		_, _ = writer.body.Write(value[:min(remaining, len(value))])
	}
	return writer.destination.Write(value)
}

func graphQLOutcome(ctx context.Context, operationType ast.Operation, status int, body []byte, truncated bool) string {
	if status == 0 || truncated {
		return "unknown"
	}
	if status == stdhttp.StatusUnauthorized || status == stdhttp.StatusForbidden || status == stdhttp.StatusTooManyRequests {
		return "rejected"
	}
	if status >= 400 {
		return "failed"
	}
	if operationType == ast.Mutation {
		if graphQLMutationOutcomeFailed(ctx) {
			return "failed"
		}
		return "succeeded"
	}
	var result struct {
		Errors []json.RawMessage `json:"errors"`
		Data   json.RawMessage   `json:"data"`
	}
	if json.Unmarshal(body, &result) != nil || len(result.Errors) != 0 || len(result.Data) == 0 || string(result.Data) == "null" {
		return "failed"
	}
	return "succeeded"
}
