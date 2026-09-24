package handlers

import (
	"reflect"
	"testing"
)

// The HTTP transport must expose only the edge operations mounted by the
// router. Reintroducing a domain REST method makes it too easy to remount an
// unreviewed parallel API beside the GraphQL domain contract.
func TestServerDoesNotExposeRetiredDomainRESTHandlers(t *testing.T) {
	server := reflect.TypeOf(&Server{})
	for _, method := range []string{
		"ListProviders", "GetActivities", "RequestMagicLink",
		"BeginPasskeyRegistration", "FinishPasskeyRegistration",
		"BeginPasskeySignIn", "FinishPasskeySignIn",
		"GetCurrentSession", "ListSessions", "RevokeOtherSessions",
		"RevokeSession", "GetCurrentUser", "SignOut", "SubjectOperation",
		"ListConnections", "CreateConnection", "GetConnection",
		"UpdateConnection", "DeleteConnection", "SyncConnection", "GetSyncJob",
		"ListCustomProviders", "CreateCustomProvider", "GetCustomProvider",
		"UpdateCustomProvider", "DeleteCustomProvider", "RotateCustomProviderKey",
	} {
		if _, exists := server.MethodByName(method); exists {
			t.Errorf("retired domain REST handler %s is still exposed", method)
		}
	}
}

func TestHTTPDependenciesDoNotCarryRetiredDomainRESTPorts(t *testing.T) {
	dependencies := reflect.TypeOf(Dependencies{})
	for _, field := range []string{"Catalog", "Sync", "Subjects", "SubjectDeletions"} {
		if _, exists := dependencies.FieldByName(field); exists {
			t.Errorf("retired REST-only dependency %s is still present", field)
		}
	}
}
