// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sourcegen

import (
	"fmt"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
)

// Registry maps origin types to their corresponding [SourceGenerator] implementations.
type Registry struct {
	generators map[projectconfig.OriginType]SourceGenerator
}

// NewRegistry creates an empty [Registry].
func NewRegistry() *Registry {
	return &Registry{
		generators: make(map[projectconfig.OriginType]SourceGenerator),
	}
}

// Register adds a generator for the given origin type. Panics if a generator
// is already registered for that type (indicates a programming error).
func (r *Registry) Register(originType projectconfig.OriginType, gen SourceGenerator) {
	if _, exists := r.generators[originType]; exists {
		panic(fmt.Sprintf("duplicate source generator registered for origin type %#q", originType))
	}

	r.generators[originType] = gen
}

// Get returns the generator registered for the given origin type, or an error
// if no generator is registered.
func (r *Registry) Get(originType projectconfig.OriginType) (SourceGenerator, error) {
	gen, ok := r.generators[originType]
	if !ok {
		return nil, fmt.Errorf("no source generator registered for origin type %#q", originType)
	}

	return gen, nil
}

// Has reports whether a generator is registered for the given origin type.
func (r *Registry) Has(originType projectconfig.OriginType) bool {
	_, ok := r.generators[originType]

	return ok
}
