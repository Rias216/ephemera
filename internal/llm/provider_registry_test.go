package llm

import (
	"context"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
)

type registryTestProvider struct{ name string }

func (provider registryTestProvider) Name() string { return provider.name }
func (provider registryTestProvider) Generate(context.Context, Request) (string, error) {
	return "ok", nil
}
func (provider registryTestProvider) ListModels(context.Context) ([]string, error) {
	return []string{"registry-model"}, nil
}

func TestProviderRegistryConstructsRegisteredAdapters(t *testing.T) {
	registry := NewProviderRegistry()
	if err := registry.Register("test-provider", func(cfg config.Config) (Provider, error) {
		return registryTestProvider{name: cfg.Provider}, nil
	}); err != nil {
		t.Fatal(err)
	}
	provider, err := registry.New(config.Config{ProviderSettings: config.ProviderSettings{Provider: "test-provider"}})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "test-provider" {
		t.Fatalf("provider name = %q", provider.Name())
	}
	if names := registry.Names(); len(names) != 1 || names[0] != "test-provider" {
		t.Fatalf("registry names = %#v", names)
	}
	models, err := registry.ListModels(context.Background(), config.Config{ProviderSettings: config.ProviderSettings{Provider: "test-provider"}})
	if err != nil || len(models) != 1 || models[0] != "registry-model" {
		t.Fatalf("registry model catalog = %#v, %v", models, err)
	}
	if err := registry.Register("test-provider", func(config.Config) (Provider, error) { return registryTestProvider{}, nil }); err == nil {
		t.Fatal("duplicate provider registration succeeded")
	}
	if err := registry.Register("bad provider", func(config.Config) (Provider, error) { return registryTestProvider{}, nil }); err == nil {
		t.Fatal("invalid provider registration succeeded")
	}
}
