// Package modelcatalog provides the provider-neutral model catalog shown by
// ONR's admin and user meter pages.
package modelcatalog

import (
	"errors"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Pricing struct {
	Input    string `yaml:"input,omitempty" json:"input"`
	Output   string `yaml:"output,omitempty" json:"output"`
	CacheHit string `yaml:"cache_hit,omitempty" json:"cache_hit"`
	Billing  string `yaml:"billing,omitempty" json:"billing"`
	Unit     string `yaml:"unit,omitempty" json:"unit"`
	Source   string `yaml:"source,omitempty" json:"source"`
}

type ProviderMapping struct {
	Supported bool   `yaml:"supported" json:"supported"`
	ModelID   string `yaml:"model_id,omitempty" json:"model_id,omitempty"`
	Note      string `yaml:"note,omitempty" json:"note,omitempty"`
}

type Model struct {
	ID        string                     `yaml:"id" json:"id"`
	Provider  string                     `yaml:"provider" json:"provider"`
	Pricing   Pricing                    `yaml:"pricing" json:"pricing"`
	Providers map[string]ProviderMapping `yaml:"providers" json:"providers"`
}

type File struct {
	Models []Model `yaml:"models"`
}

type Catalog struct {
	models []Model
}

func Load(path string) (*Catalog, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return &Catalog{}, nil
	}
	b, err := os.ReadFile(path) // #nosec G304 -- catalog path is trusted configuration.
	if err != nil {
		return nil, err
	}
	var file File
	if err := yaml.Unmarshal(b, &file); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(file.Models))
	out := make([]Model, 0, len(file.Models))
	for _, model := range file.Models {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			return nil, errors.New("model catalog contains an empty model id")
		}
		if _, ok := seen[model.ID]; ok {
			return nil, errors.New("duplicate model id: " + model.ID)
		}
		seen[model.ID] = struct{}{}
		model.Provider = strings.TrimSpace(model.Provider)
		if model.Providers == nil {
			model.Providers = map[string]ProviderMapping{}
		}
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return &Catalog{models: out}, nil
}

func (c *Catalog) Models() []Model {
	if c == nil {
		return nil
	}
	out := make([]Model, len(c.models))
	copy(out, c.models)
	return out
}

func (c *Catalog) OpenAIList() map[string]any {
	data := make([]map[string]any, 0, len(c.Models()))
	for _, model := range c.Models() {
		data = append(data, map[string]any{"id": model.ID, "object": "model", "owned_by": model.Provider})
	}
	return map[string]any{"object": "list", "data": data}
}
