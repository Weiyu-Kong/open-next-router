package models

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Strategy string

const (
	StrategyRoundRobin Strategy = "round_robin"
	StrategyPriority   Strategy = "priority"
)

type Route struct {
	Providers []string `yaml:"providers"`
	Strategy  Strategy `yaml:"strategy"`
	OwnedBy   string   `yaml:"owned_by"`
}

type File struct {
	ProviderPriority []string         `yaml:"provider_priority,omitempty"`
	Models           map[string]Route `yaml:"models"`
}

// Router holds model -> providers routing and per-model round-robin state.
type Router struct {
	mu               sync.Mutex
	routes           map[string]Route
	nextIdx          map[string]int
	providerPriority []string
}

// NewRouter returns a non-nil router.
func NewRouter(routes map[string]Route) *Router {
	return NewRouterWithPriority(routes, nil)
}

// NewRouterWithPriority constructs a router whose model routes follow the
// configured global provider priority. Providers absent from the priority list
// remain available after the explicitly ordered providers.
func NewRouterWithPriority(routes map[string]Route, providerPriority []string) *Router {
	out := &Router{
		routes:           map[string]Route{},
		nextIdx:          map[string]int{},
		providerPriority: normalizeProviders(providerPriority),
	}
	for id, r := range routes {
		mid := normalizeModelID(id)
		if mid == "" {
			continue
		}
		route := normalizeRoute(r)
		if len(out.providerPriority) > 0 {
			route.Providers = orderProviders(route.Providers, out.providerPriority)
			route.Strategy = StrategyPriority
		}
		out.routes[mid] = route
	}
	return out
}

// ProviderPriority returns a copy of the configured global priority.
func (r *Router) ProviderPriority() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.providerPriority...)
}

// Models requires a non-nil Router receiver.
func (r *Router) Models() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.routes))
	for id := range r.routes {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// RouteForModel returns the normalized route for a model, when configured.
func (r *Router) RouteForModel(modelID string) (Route, bool) {
	id := normalizeModelID(modelID)
	if id == "" {
		return Route{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rt, ok := r.routes[id]
	if !ok {
		return Route{}, false
	}
	return normalizeRoute(rt), true
}

// NextProvider requires a non-nil Router receiver.
func (r *Router) NextProvider(modelID string) (string, bool) {
	return r.NextProviderAllowed(modelID, nil)
}

// NextProviderAllowed selects a provider that both supports the model and is
// allowed by the current Access Key. An empty allowlist permits every provider.
func (r *Router) NextProviderAllowed(modelID string, allowed []string) (string, bool) {
	id := normalizeModelID(modelID)
	if id == "" {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rt, ok := r.routes[id]
	if !ok || len(rt.Providers) == 0 {
		return "", false
	}
	providers := filterAllowedProviders(rt.Providers, allowed)
	if len(providers) == 0 {
		return "", false
	}
	strategy := rt.Strategy
	if strategy == "" {
		strategy = StrategyRoundRobin
	}
	switch strategy {
	case StrategyPriority:
		return providers[0], true
	case StrategyRoundRobin:
		i := r.nextIdx[id] % len(providers)
		r.nextIdx[id] = (i + 1) % len(providers)
		return providers[i], true
	default:
		// Unknown strategy: fall back to round-robin for forward compatibility.
		i := r.nextIdx[id] % len(providers)
		r.nextIdx[id] = (i + 1) % len(providers)
		return providers[i], true
	}
}

func (r *Router) ToOpenAIList() map[string]any {
	return r.ToOpenAIListAt(0)
}

func (r *Router) ToOpenAIListAt(createdAtUnix int64) map[string]any {
	created := createdAtUnix
	if created <= 0 {
		created = 0
	}
	data := make([]any, 0)
	for _, id := range r.Models() {
		data = append(data, map[string]any{
			"id":       id,
			"created":  created,
			"owned_by": "custom",
		})
	}
	return map[string]any{
		"object": "list",
		"data":   data,
	}
}

// Load reads a model routing file. If the file does not exist, returns an empty router and nil error.
func Load(path string) (*Router, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return NewRouter(nil), nil
	}
	// #nosec G304 -- path is provided by trusted config.
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewRouter(nil), nil
		}
		return nil, err
	}
	f, err := parseFile(b)
	if err != nil {
		return nil, err
	}
	return NewRouterWithPriority(f.Models, f.ProviderPriority), nil
}

// LoadFile reads the editable model routing document.
func LoadFile(path string) (File, error) {
	b, err := os.ReadFile(strings.TrimSpace(path)) // #nosec G304 -- trusted configuration path.
	if err != nil {
		return File{}, err
	}
	return parseFile(b)
}

// UpdateProviderPriority atomically updates only the global priority while
// preserving all model routes semantically.
func UpdateProviderPriority(path string, priority []string) error {
	p := strings.TrimSpace(path)
	if p == "" {
		return errors.New("models file path is empty")
	}
	f, err := LoadFile(p)
	if err != nil {
		return err
	}
	f.ProviderPriority = normalizeProviders(priority)
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".models-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, p)
}

func parseFile(b []byte) (File, error) {
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return File{}, err
	}
	f.ProviderPriority = normalizeProviders(f.ProviderPriority)
	return f, nil
}

func normalizeModelID(s string) string {
	return strings.TrimSpace(s)
}

func normalizeRoute(r Route) Route {
	out := r
	out.OwnedBy = strings.TrimSpace(out.OwnedBy)
	if out.Strategy == "" {
		out.Strategy = StrategyRoundRobin
	}
	provs := make([]string, 0, len(out.Providers))
	for _, p := range out.Providers {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		provs = append(provs, p)
	}
	out.Providers = provs
	return out
}

func normalizeProviders(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func orderProviders(providers, priority []string) []string {
	rank := make(map[string]int, len(priority))
	for i, provider := range priority {
		rank[provider] = i
	}
	out := append([]string(nil), providers...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, iOK := rank[out[i]]
		rj, jOK := rank[out[j]]
		if iOK != jOK {
			return iOK
		}
		if !iOK {
			return false
		}
		return ri < rj
	})
	return out
}

func filterAllowedProviders(providers, allowed []string) []string {
	if len(allowed) == 0 {
		return append([]string(nil), providers...)
	}
	set := make(map[string]struct{}, len(allowed))
	for _, provider := range normalizeProviders(allowed) {
		set[provider] = struct{}{}
	}
	out := make([]string, 0, len(providers))
	for _, provider := range providers {
		if _, ok := set[provider]; ok {
			out = append(out, provider)
		}
	}
	return out
}
