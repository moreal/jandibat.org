package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	gqlgen "github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
)

const (
	defaultMaxGraphQLBody = 64 << 10
	maxGraphQLDepth       = 12
	maxGraphQLFields      = 200
	maxGraphQLCost        = 1000
	maxOperationNameBytes = 64
)

// HTTPOptions applies to both the outer preflight middleware and handler.
// Development must only be enabled by an explicit local development setting.
type HTTPOptions struct {
	Development  bool
	MaxBodyBytes int64
}

// OperationMetadata is safe for audit, rate-limit and metrics labels. Only the
// GraphQL operation name/type and bounded numerical estimates appear here; it
// never contains source text, variables, IDs or request credentials.
type OperationMetadata struct {
	Name  string
	Type  ast.Operation
	Cost  int
	Depth int
}

type operationMetadataKey struct{}
type preflightCompleteKey struct{}
type operationOutcomeKey struct{}

// OperationOutcome is the trusted, selection-independent result for audit.
// A mutation is successful only when Executed is true and Failed is false.
// It never contains user input, payload data or error messages.
type OperationOutcome struct {
	Executed bool
	Failed   bool
}

type operationOutcomeState struct {
	executed atomic.Bool
	failed   atomic.Bool
}

func OperationOutcomeFromContext(ctx context.Context) (OperationOutcome, bool) {
	state, ok := ctx.Value(operationOutcomeKey{}).(*operationOutcomeState)
	if !ok || state == nil {
		return OperationOutcome{}, false
	}
	return OperationOutcome{Executed: state.executed.Load(), Failed: state.failed.Load()}, true
}

func OperationMetadataFromContext(ctx context.Context) (OperationMetadata, bool) {
	meta, ok := ctx.Value(operationMetadataKey{}).(OperationMetadata)
	return meta, ok
}

// PreflightHTTP must be installed before audit and rate-limit middleware so
// those controls can read OperationMetadataFromContext. It only handles the
// exact /graphql path; HTTP edge routes pass through unchanged.
func PreflightHTTP(next http.Handler, options HTTPOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" || r.Method == http.MethodOptions || r.Context().Value(preflightCompleteKey{}) == true {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writePreflightError(w, http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Upgrade") != "" {
			writePreflightError(w, http.StatusBadRequest)
			return
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" || !validJSONCharset(params) {
			writePreflightError(w, http.StatusUnsupportedMediaType)
			return
		}
		limit := options.MaxBodyBytes
		if limit <= 0 || limit > defaultMaxGraphQLBody {
			limit = defaultMaxGraphQLBody
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			writePreflightError(w, http.StatusBadRequest)
			return
		}
		if int64(len(body)) > limit {
			writePreflightError(w, http.StatusRequestEntityTooLarge)
			return
		}
		if !utf8.Valid(body) {
			writePreflightError(w, http.StatusBadRequest)
			return
		}
		request, err := decodeGraphQLRequest(body)
		if err != nil {
			writePreflightError(w, http.StatusBadRequest)
			return
		}
		metadata, err := inspectGraphQLOperation(request, options.Development)
		if err != nil {
			writePreflightError(w, http.StatusBadRequest)
			return
		}
		ctx := context.WithValue(r.Context(), operationMetadataKey{}, metadata)
		ctx = context.WithValue(ctx, operationOutcomeKey{}, &operationOutcomeState{})
		ctx = context.WithValue(ctx, preflightCompleteKey{}, true)
		r = r.WithContext(ctx)
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

func validJSONCharset(params map[string]string) bool {
	for key, value := range params {
		if strings.EqualFold(key, "charset") && strings.EqualFold(value, "utf-8") {
			continue
		}
		return false
	}
	return true
}

type graphQLRequest struct {
	Query         string          `json:"query"`
	OperationName string          `json:"operationName"`
	Variables     json.RawMessage `json:"variables"`
	Extensions    json.RawMessage `json:"extensions"`
}

func decodeGraphQLRequest(body []byte) (graphQLRequest, error) {
	var request graphQLRequest
	if len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '{' {
		return request, errors.New("not a single object")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return request, errors.New("trailing JSON")
	}
	if request.Query == "" {
		return request, errors.New("missing query")
	}
	for _, raw := range []json.RawMessage{request.Variables, request.Extensions} {
		if len(raw) != 0 && string(raw) != "null" && bytes.TrimSpace(raw)[0] != '{' {
			return request, errors.New("invalid parameters")
		}
	}
	return request, nil
}

func inspectGraphQLOperation(request graphQLRequest, development bool) (OperationMetadata, error) {
	var result OperationMetadata
	query, err := parser.ParseQueryWithTokenLimit(&ast.Source{Input: request.Query}, 4096)
	if err != nil {
		return result, err
	}
	operation := query.Operations.ForName(request.OperationName)
	if operation == nil || operation.Operation == ast.Subscription || !development && operation.Name == "" || len(operation.Name) > maxOperationNameBytes {
		return result, errors.New("operation rejected")
	}
	if operation.Operation != ast.Query && operation.Operation != ast.Mutation {
		return result, errors.New("operation rejected")
	}
	var variables map[string]json.RawMessage
	if len(request.Variables) != 0 && string(request.Variables) != "null" {
		if err := json.Unmarshal(request.Variables, &variables); err != nil {
			return result, err
		}
	}
	inspector := operationInspector{document: query, variables: variables, defaults: make(map[string]*ast.Value), development: development}
	for _, definition := range operation.VariableDefinitions {
		inspector.defaults[definition.Variable] = definition.DefaultValue
	}
	if err := inspector.walk(operation.SelectionSet, 1, 1, make(map[string]bool), true); err != nil {
		return result, err
	}
	if operation.Operation == ast.Mutation && inspector.rootFields != 1 {
		return result, errors.New("compound mutation")
	}
	result = OperationMetadata{Name: operation.Name, Type: operation.Operation, Cost: inspector.cost, Depth: inspector.depth}
	return result, nil
}

type operationInspector struct {
	document    *ast.QueryDocument
	variables   map[string]json.RawMessage
	defaults    map[string]*ast.Value
	development bool
	fields      int
	rootFields  int
	cost        int
	depth       int
}

func (i *operationInspector) walk(selections ast.SelectionSet, depth, multiplier int, visiting map[string]bool, root bool) error {
	if depth > maxGraphQLDepth {
		return errors.New("query too deep")
	}
	for _, selection := range selections {
		switch item := selection.(type) {
		case *ast.Field:
			if root {
				i.rootFields++
			}
			if !i.development && (item.Name == "__schema" || item.Name == "__type") {
				return errors.New("introspection disabled")
			}
			i.fields++
			if i.fields > maxGraphQLFields {
				return errors.New("too many fields")
			}
			if depth > i.depth {
				i.depth = depth
			}
			weight := multiplier
			if item.Name == "activitySnapshot" {
				weight *= 100
			}
			if isConnectionField(item.Name) {
				first := 100 // Missing `first` uses a bounded, conservative estimate.
				if argument := item.Arguments.ForName("first"); argument != nil {
					var err error
					first, err = i.first(argument.Value)
					if err != nil {
						return err
					}
				}
				weight *= first
			}
			i.cost += weight
			if i.cost > maxGraphQLCost || weight > maxGraphQLCost {
				return errors.New("query too costly")
			}
			if err := i.walk(item.SelectionSet, depth+1, weight, visiting, false); err != nil {
				return err
			}
		case *ast.InlineFragment:
			if err := i.walk(item.SelectionSet, depth, multiplier, visiting, root); err != nil {
				return err
			}
		case *ast.FragmentSpread:
			if visiting[item.Name] {
				return errors.New("cyclic fragment")
			}
			fragment := i.document.Fragments.ForName(item.Name)
			if fragment == nil {
				return errors.New("unknown fragment")
			}
			visiting[item.Name] = true
			err := i.walk(fragment.SelectionSet, depth+1, multiplier, visiting, root)
			delete(visiting, item.Name)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (i *operationInspector) first(value *ast.Value) (int, error) {
	if value == nil {
		return 0, errors.New("invalid page size")
	}
	var raw string
	switch value.Kind {
	case ast.IntValue:
		raw = value.Raw
	case ast.Variable:
		encoded, ok := i.variables[value.Raw]
		if !ok {
			if fallback := i.defaults[value.Raw]; fallback != nil && fallback.Kind == ast.IntValue {
				raw = fallback.Raw
				break
			}
			return 0, errors.New("missing page size")
		}
		if err := json.Unmarshal(encoded, &raw); err != nil {
			raw = string(encoded)
		}
	case ast.FloatValue, ast.StringValue, ast.BlockValue, ast.BooleanValue, ast.NullValue,
		ast.EnumValue, ast.ListValue, ast.ObjectValue:
		return 0, errors.New("invalid page size")
	default: // Reject future gqlparser value kinds until explicitly reviewed.
		return 0, errors.New("invalid page size")
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1 || count > 100 {
		return 0, errors.New("invalid page size")
	}
	return count, nil
}

func isConnectionField(name string) bool {
	switch name {
	case "subjects", "sessions", "providerConnections", "customProviders", "syncJobs":
		return true
	default:
		return false
	}
}

func writePreflightError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"errors":[{"message":"GraphQL request rejected."}]}`)
}

// NewHTTPHandler is POST-only. It is safe to mount behind an outer
// PreflightHTTP (for operation-aware middleware) or standalone in tests.
func NewHTTPHandler(resolver *Resolver, options HTTPOptions) http.Handler {
	server := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: resolver}))
	server.AddTransport(transport.POST{})
	server.AroundFields(func(ctx context.Context, next gqlgen.Resolver) (any, error) {
		field := gqlgen.GetFieldContext(ctx)
		if field == nil || field.Object != "Mutation" {
			return next(ctx)
		}
		state, _ := ctx.Value(operationOutcomeKey{}).(*operationOutcomeState)
		value, err := next(ctx)
		if state != nil {
			state.executed.Store(true)
			payload, ok := value.(model.MutationPayload)
			if err != nil || !ok || value == nil || reflect.ValueOf(value).Kind() == reflect.Ptr && reflect.ValueOf(value).IsNil() {
				state.failed.Store(true)
			} else if len(payload.GetErrors()) != 0 {
				state.failed.Store(true)
			}
		}
		return value, err
	})
	server.AroundResponses(func(ctx context.Context, next gqlgen.ResponseHandler) *gqlgen.Response {
		response := next(ctx)
		if state, _ := ctx.Value(operationOutcomeKey{}).(*operationOutcomeState); state != nil && response != nil && len(response.Errors) != 0 {
			state.failed.Store(true)
		}
		return response
	})
	if options.Development {
		server.Use(extension.Introspection{})
	}
	server.SetParserTokenLimit(4096)
	server.SetErrorPresenter(presentGraphQLError)
	server.SetRecoverFunc(func(_ context.Context, _ any) error {
		return errors.New("internal server error")
	})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.ServeHTTP(w, r)
		if metadata, ok := OperationMetadataFromContext(r.Context()); ok && metadata.Type == ast.Mutation {
			if state, ok := r.Context().Value(operationOutcomeKey{}).(*operationOutcomeState); ok && !state.executed.Load() {
				state.failed.Store(true)
			}
		}
	})
	return PreflightHTTP(inner, options)
}

// This is intentionally a closed mapping: even a resolver-supplied gqlerror
// with an approved code cannot forward an arbitrary message or extension.
func presentGraphQLError(_ context.Context, err error) *gqlerror.Error {
	var candidate *gqlerror.Error
	if errors.As(err, &candidate) {
		if code, ok := candidate.Extensions["code"].(string); ok {
			var message string
			switch code {
			case "BAD_USER_INPUT":
				message = "Invalid input."
			case "INVALID_DATE_RANGE":
				message = "Invalid date range."
			case "RANGE_TOO_LARGE":
				message = "Date range is too large."
			case "INVALID_ENVIRONMENT":
				message = "Invalid environment selection."
			case "PROVIDER_UNAVAILABLE":
				message = "Activity provider unavailable."
			}
			if message != "" {
				return &gqlerror.Error{Message: message, Extensions: map[string]any{"code": code}}
			}
		}
	}
	return &gqlerror.Error{Message: "GraphQL request failed."}
}
