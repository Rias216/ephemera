package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ephemera-ai/ephemera/internal/config"
)

// ProviderFactory constructs one configured provider adapter.
type ProviderFactory func(config.Config) (Provider, error)

// ProviderRegistry is the provider-construction boundary used by the runtime.
// Adapters register factories without introducing provider-specific branches in
// the agent or the default constructor.
type ProviderRegistry interface {
	Register(string, ProviderFactory) error
	New(config.Config) (Provider, error)
	ListModels(context.Context, config.Config) ([]string, error)
	Names() []string
}

type providerCatalog struct {
	mu        sync.RWMutex
	factories map[string]ProviderFactory
}

// NewProviderRegistry creates an isolated provider registry for tests,
// embedders, and alternate application compositions.
func NewProviderRegistry() ProviderRegistry {
	return &providerCatalog{factories: map[string]ProviderFactory{}}
}

func (registry *providerCatalog) Register(name string, factory ProviderFactory) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if !config.ValidProvider(name) {
		return fmt.Errorf("provider name %q is invalid", name)
	}
	if factory == nil {
		return fmt.Errorf("provider factory is required for %s", name)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.factories[name]; exists {
		return fmt.Errorf("provider %q is already registered", name)
	}
	registry.factories[name] = factory
	return nil
}

func (registry *providerCatalog) New(cfg config.Config) (Provider, error) {
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	registry.mu.RLock()
	factory := registry.factories[name]
	registry.mu.RUnlock()
	if factory == nil {
		return nil, fmt.Errorf("unsupported provider %q (registered: %s)", cfg.Provider, strings.Join(registry.Names(), ", "))
	}
	return factory(cfg)
}

func (registry *providerCatalog) ListModels(ctx context.Context, cfg config.Config) ([]string, error) {
	provider, err := registry.New(cfg)
	if err != nil {
		return nil, err
	}
	catalog, ok := provider.(ModelCatalogProvider)
	if !ok {
		return nil, fmt.Errorf("provider %q does not expose a model catalog", provider.Name())
	}
	return catalog.ListModels(ctx)
}

func (registry *providerCatalog) Names() []string {
	registry.mu.RLock()
	names := make([]string, 0, len(registry.factories))
	for name := range registry.factories {
		names = append(names, name)
	}
	registry.mu.RUnlock()
	sort.Strings(names)
	return names
}

var defaultProviderRegistry = NewProviderRegistry()

// RegisterProvider adds an adapter factory to the process-wide registry.
// Provider source files call it from init so provider.go remains provider-free.
func RegisterProvider(name string, factory ProviderFactory) {
	if err := defaultProviderRegistry.Register(name, factory); err != nil {
		panic("llm: " + err.Error())
	}
}

// New constructs the active provider through the registration catalog.
func New(cfg config.Config) (Provider, error) {
	return defaultProviderRegistry.New(cfg)
}

// ProviderNames returns registered provider IDs in deterministic order.
func ProviderNames() []string {
	return defaultProviderRegistry.Names()
}
