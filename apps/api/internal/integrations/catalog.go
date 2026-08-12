package integrations

import (
	"context"
	"sort"
)

var builtInProviders = []ProviderCatalogItem{
	{
		ID:                  "github",
		Name:                "GitHub",
		Description:         "GitHub public contribution activity",
		Kind:                ProviderGitHub,
		Category:            CategoryGitHosting,
		SupportsOAuth:       true,
		SupportsToken:       true,
		SupportsPrivateData: false,
	},
	{
		ID:                  "gitlab",
		Name:                "GitLab",
		Description:         "GitLab contribution activity",
		Kind:                ProviderGitLab,
		Category:            CategoryGitHosting,
		SupportsOAuth:       true,
		SupportsToken:       true,
		SupportsPrivateData: true,
	},
	{
		ID:                  "codeberg",
		Name:                "Codeberg",
		Description:         "Codeberg contribution activity",
		Kind:                ProviderCodeberg,
		Category:            CategoryGitHosting,
		SupportsOAuth:       true,
		SupportsToken:       true,
		SupportsPrivateData: false,
	},
}

type CatalogService struct {
	customProviders CustomProviderStore
}

func NewCatalogService(customProviders CustomProviderStore) *CatalogService {
	return &CatalogService{customProviders: customProviders}
}

func (service *CatalogService) List(ctx context.Context, subjectID string) ([]ProviderCatalogItem, error) {
	items := append([]ProviderCatalogItem(nil), builtInProviders...)
	if service.customProviders != nil && subjectID != "" {
		records, err := service.customProviders.ListCustomProviders(ctx, subjectID)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			provider := record.Provider
			items = append(items, ProviderCatalogItem{
				ID:               "custom:" + provider.ID,
				Name:             provider.Name,
				Description:      provider.Description,
				Kind:             ProviderCustom,
				Category:         CategoryCustom,
				SupportsToken:    true,
				CustomProviderID: provider.ID,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Category != items[j].Category {
			return items[i].Category < items[j].Category
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func BuiltInProvider(id string) (ProviderCatalogItem, bool) {
	for _, provider := range builtInProviders {
		if provider.ID == id {
			return provider, true
		}
	}
	return ProviderCatalogItem{}, false
}
