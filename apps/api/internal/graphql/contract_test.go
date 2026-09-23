package graphql

import (
	"testing"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/generated"
)

func TestContractRootsExecuteWithoutPlaceholders(t *testing.T) {
	server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}))
	graph := client.New(server)

	var query struct {
		Contract bool `json:"_contract"`
	}
	graph.MustPost(`query Contract { _contract }`, &query)
	if !query.Contract {
		t.Fatal("query contract root returned false")
	}

	var mutation struct {
		Contract bool `json:"_contract"`
	}
	graph.MustPost(`mutation Contract { _contract }`, &mutation)
	if !mutation.Contract {
		t.Fatal("mutation contract root returned false")
	}
}
