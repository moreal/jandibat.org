package integrations

import (
	"context"
	"testing"
)

func TestCatalogDoesNotAdvertiseUnimplementedPrivateActivity(t *testing.T) {
	items, err := NewCatalogService(nil).List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	wantPrivate := map[string]bool{"github": false, "gitlab": true, "codeberg": false}
	for _, item := range items {
		if want, ok := wantPrivate[item.ID]; ok && item.SupportsPrivateData != want {
			t.Errorf("%s supportsPrivateData=%t, want %t", item.ID, item.SupportsPrivateData, want)
		}
	}
}
