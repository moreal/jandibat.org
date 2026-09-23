package apihttp

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	graph "github.com/moreal/jandibat.org/apps/api/internal/graphql"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

const maxGraphQLRateLimitBody = 64 << 10

type graphQLRateBucket struct{ scope, key string }

// graphQLRateLimit runs after bounded GraphQL preflight and before execution.
// The client-supplied operation name is never used to choose a policy: an
// alias or fragment still resolves to the selected mutation field.
func graphQLRateLimit(w http.ResponseWriter, r *http.Request, limiter handlers.RateLimiter) bool {
	if r.URL.Path != "/graphql" {
		return true
	}
	metadata, ok := graph.OperationMetadataFromContext(r.Context())
	if !ok || limiter == nil {
		graphQLRateUnavailable(w, r)
		return false
	}
	ip := normalizedRemoteIP(r.RemoteAddr)
	var buckets []graphQLRateBucket
	if metadata.Type == ast.Query {
		buckets = append(buckets, graphQLRateBucket{"public_activity_ip", ip})
	} else if metadata.Type == ast.Mutation {
		field, input, err := graphQLMutationInput(r, metadata.Name)
		if err != nil {
			graphQLRateUnavailable(w, r)
			return false
		}
		switch field {
		case "requestMagicLink":
			buckets = append(buckets, graphQLRateBucket{"magic_link_ip", ip})
			if email, ok := input["email"].(string); ok && strings.TrimSpace(email) != "" {
				key := opaqueMutationIdentity("email", strings.ToLower(strings.TrimSpace(email)))
				buckets = append(buckets, graphQLRateBucket{"magic_link_account", key})
				buckets = append(buckets, graphQLRateBucket{"magic_link_ip_daily", ip}, graphQLRateBucket{"magic_link_account_daily", key})
			} else {
				buckets = append(buckets, graphQLRateBucket{"magic_link_ip_daily", ip})
			}
		case "beginPasskeyRegistration", "finishPasskeyRegistration", "beginPasskeySignIn", "finishPasskeySignIn":
			buckets = append(buckets, graphQLRateBucket{"passkey_ip", ip})
			if email, ok := input["email"].(string); ok && strings.TrimSpace(email) != "" {
				buckets = append(buckets, graphQLRateBucket{"passkey_email", opaqueMutationIdentity("email", strings.ToLower(strings.TrimSpace(email)))})
			}
			if ceremony, ok := input["ceremonyID"].(string); ok && strings.TrimSpace(ceremony) != "" {
				buckets = append(buckets, graphQLRateBucket{"passkey_ceremony", opaqueMutationIdentity("passkey-ceremony", ceremony)})
			}
		case "connectProvider", "updateProviderConnection", "revokeProviderConnection", "createCustomProvider", "updateCustomProvider", "deleteCustomProvider":
			buckets = append(buckets, graphQLRateBucket{"connection_ip", ip})
		case "enqueueManualSync":
			buckets = append(buckets, graphQLRateBucket{"sync_ip", ip})
		case "rotateCustomProviderKey":
			buckets = append(buckets, graphQLRateBucket{"rotate_key_ip", ip})
		}
	}
	return allowGraphQLRateBuckets(w, r, limiter, buckets)
}

// graphQLVerifiedAccountRateLimit runs only after the trusted HTTP boundary
// has verified the session and resolved its user. It prevents rotating session
// credentials from resetting account read or mutation quotas.
func graphQLVerifiedAccountRateLimit(w http.ResponseWriter, r *http.Request, limiter handlers.RateLimiter, userID string) bool {
	if r.URL.Path != "/graphql" {
		return true
	}
	metadata, ok := graph.OperationMetadataFromContext(r.Context())
	if !ok || limiter == nil || userID == "" {
		graphQLRateUnavailable(w, r)
		return false
	}
	accountKey := opaqueMutationIdentity("user", userID)
	if metadata.Type == ast.Query {
		return allowGraphQLRateBuckets(w, r, limiter, []graphQLRateBucket{{"public_activity_account", accountKey}})
	}
	if metadata.Type != ast.Mutation {
		return true
	}
	field, _, err := graphQLMutationInput(r, metadata.Name)
	if err != nil {
		graphQLRateUnavailable(w, r)
		return false
	}
	buckets := []graphQLRateBucket{{"mutation_intent_session", accountKey}}
	switch field {
	case "beginPasskeyRegistration", "finishPasskeyRegistration", "beginPasskeySignIn", "finishPasskeySignIn":
		buckets = append(buckets, graphQLRateBucket{"passkey_account", accountKey})
	case "connectProvider", "updateProviderConnection", "revokeProviderConnection", "createCustomProvider", "updateCustomProvider", "deleteCustomProvider":
		buckets = append(buckets, graphQLRateBucket{"connection_account", accountKey})
	case "enqueueManualSync":
		buckets = append(buckets, graphQLRateBucket{"sync_account", accountKey})
	case "rotateCustomProviderKey":
		buckets = append(buckets, graphQLRateBucket{"rotate_key_account", accountKey})
	}
	return allowGraphQLRateBuckets(w, r, limiter, buckets)
}

func allowGraphQLRateBuckets(w http.ResponseWriter, r *http.Request, limiter handlers.RateLimiter, buckets []graphQLRateBucket) bool {
	for _, bucket := range buckets {
		allowed, retryAfter, err := limiter.Allow(r.Context(), bucket.scope, bucket.key)
		if err != nil {
			graphQLRateUnavailable(w, r)
			return false
		}
		if !allowed {
			seconds := int(math.Ceil(retryAfter.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeFrameworkProblem(w, r, http.StatusTooManyRequests, "Too Many Requests", "rate_limited", "Too many requests were received. Try again later.")
			return false
		}
	}
	return true
}

func graphQLRateUnavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "5")
	writeFrameworkProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "service_unavailable", "The service cannot safely process this request right now.")
}

// The preflight restores the bounded body. Restore it again after this narrow
// inspection so gqlgen receives the exact original bytes. Never log the body.
func graphQLMutationInput(r *http.Request, operationName string) (string, map[string]any, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxGraphQLRateLimitBody+1))
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > maxGraphQLRateLimitBody {
		return "", nil, io.ErrUnexpectedEOF
	}
	var request struct {
		Query         string         `json:"query"`
		OperationName string         `json:"operationName"`
		Variables     map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "", nil, err
	}
	if request.OperationName != "" && request.OperationName != operationName {
		return "", nil, io.ErrUnexpectedEOF
	}
	document, err := parser.ParseQueryWithTokenLimit(&ast.Source{Input: request.Query}, 4096)
	if err != nil {
		return "", nil, err
	}
	operation := document.Operations.ForName(request.OperationName)
	if operation == nil || operation.Name != operationName || operation.Operation != ast.Mutation {
		return "", nil, io.ErrUnexpectedEOF
	}
	field := graphQLRootField(document, operation.SelectionSet, map[string]bool{})
	if field == nil {
		return "", nil, io.ErrUnexpectedEOF
	}
	argument := field.Arguments.ForName("input")
	if argument == nil {
		return field.Name, nil, nil
	}
	for _, definition := range operation.VariableDefinitions {
		if _, exists := request.Variables[definition.Variable]; !exists && definition.DefaultValue != nil {
			value, valueErr := definition.DefaultValue.Value(request.Variables)
			if valueErr != nil {
				return "", nil, valueErr
			}
			if request.Variables == nil {
				request.Variables = make(map[string]any)
			}
			request.Variables[definition.Variable] = value
		}
	}
	value, err := argument.Value.Value(request.Variables)
	if err != nil {
		return "", nil, err
	}
	input, _ := value.(map[string]any)
	return field.Name, input, nil
}

func graphQLRootField(document *ast.QueryDocument, selections ast.SelectionSet, visited map[string]bool) *ast.Field {
	for _, selection := range selections {
		switch item := selection.(type) {
		case *ast.Field:
			return item
		case *ast.InlineFragment:
			if field := graphQLRootField(document, item.SelectionSet, visited); field != nil {
				return field
			}
		case *ast.FragmentSpread:
			if visited[item.Name] {
				continue
			}
			fragment := document.Fragments.ForName(item.Name)
			if fragment == nil {
				continue
			}
			visited[item.Name] = true
			field := graphQLRootField(document, fragment.SelectionSet, visited)
			delete(visited, item.Name)
			if field != nil {
				return field
			}
		}
	}
	return nil
}
