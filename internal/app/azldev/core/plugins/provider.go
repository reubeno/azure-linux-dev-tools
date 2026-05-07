// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package plugins

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ErrProviderNotFound is returned when [Registry.Lookup] is asked for a
// provider whose (kind, name) pair has not been registered.
var ErrProviderNotFound = errors.New("provider not found")

// ErrProviderCollision is returned by [BuildRegistry] when two plugins
// register a provider with the same (kind, name). Both plugin paths are
// included in the error message so users can disambiguate by removing
// one '--plugin' invocation.
var ErrProviderCollision = errors.New("provider collision")

// ErrProviderToolMissing is returned by [BuildRegistry] when a plugin
// declares a provider whose 'tool' name does not appear in the plugin's
// own tool catalog. This is almost always a plugin authoring bug —
// surface it loudly so the author notices.
var ErrProviderToolMissing = errors.New("provider tool missing from plugin catalog")

// ProviderRef is the on-the-wire shape that plugins use to declare a
// provider in their manifest. It binds a contract identifier (kind+name)
// to a tool the plugin advertises.
//
// JSON tags are camelCase here because nothing in this struct contains a
// dash. (We keep kebab-case for fields where the wire format already
// committed to it elsewhere in the manifest.)
type ProviderRef struct {
	// Kind names the contract this provider implements (e.g., "builder",
	// "source-provider"). The set of valid kinds is owned by azldev.
	Kind string `json:"kind"`

	// Name is a human-readable handle the user will type to select this
	// provider (e.g., '--builder=cloud'). Must be unique within Kind
	// across the loaded plugin set.
	Name string `json:"name"`

	// Tool is the name of an MCP tool advertised by the plugin that
	// implements the contract. Must appear in the plugin's tools/list
	// response.
	Tool string `json:"tool"`
}

// Provider is a resolved provider reference: the [ProviderRef] from a
// plugin's manifest, paired with the plugin handle and the resolved
// [mcp.Tool] descriptor. Consumers invoke the provider via [Provider.Call]
// rather than reaching into the plugin directly.
type Provider struct {
	// Plugin is the live plugin handle. Owned by the loader; do not Close.
	Plugin *Plugin
	// Tool is the resolved tool descriptor (matched against [ProviderRef.Tool]).
	Tool mcp.Tool
	// Ref is the original manifest declaration.
	Ref ProviderRef
}

// Call invokes the underlying tool. The args shape is defined by the
// provider kind's contract (see the provider-kind documentation).
// Mirrors [Plugin.Call] semantics for transport / tool-error wrapping.
func (p *Provider) Call(ctx context.Context, args map[string]any) (string, error) {
	return p.Plugin.Call(ctx, p.Tool.Name, args)
}

// Registry is the lookup table built from all loaded plugins' manifests.
// Use [BuildRegistry] to construct one; [Registry.Lookup] to retrieve a
// specific provider; [Registry.ListByKind] to enumerate by kind.
//
// Registry is read-only after construction — both list and lookup
// operations are safe for concurrent use.
type Registry struct {
	// byKindName is keyed by kind, then by provider name within that kind.
	byKindName map[string]map[string]*Provider
}

// BuildRegistry walks the loaded plugins, parses each manifest's
// providers list, resolves tool references, and validates non-collision
// rules. Returns a populated [Registry] (which may be empty when no
// plugin declared providers) and any validation error encountered.
//
// On error, partial state from successful plugins is discarded: callers
// receive either a fully-validated registry or an explanatory error,
// never a half-built one.
func BuildRegistry(loaded []*Plugin) (*Registry, error) {
	registry := &Registry{
		byKindName: make(map[string]map[string]*Provider),
	}

	for _, plugin := range loaded {
		if err := registerPluginProviders(registry, plugin); err != nil {
			return nil, err
		}
	}

	return registry, nil
}

// registerPluginProviders walks one plugin's manifest providers list and
// adds each entry to registry. Returns the first error encountered so
// the caller can give up early.
func registerPluginProviders(registry *Registry, plugin *Plugin) error {
	manifest := plugin.Manifest()
	if manifest == nil || len(manifest.Providers) == 0 {
		return nil
	}

	for _, ref := range manifest.Providers {
		tool, ok := lookupToolByName(plugin, ref.Tool)
		if !ok {
			return fmt.Errorf(
				"%w: plugin %#q manifest references tool %#q for provider %s/%s, "+
					"but the plugin's tools/list response does not include it",
				ErrProviderToolMissing, plugin.Name(), ref.Tool, ref.Kind, ref.Name)
		}

		provider := &Provider{
			Plugin: plugin,
			Tool:   tool,
			Ref:    ref,
		}

		if err := registry.add(provider); err != nil {
			return err
		}
	}

	return nil
}

// add records a provider, returning [ErrProviderCollision] (wrapped) if
// the same (kind, name) is already taken by another plugin.
func (r *Registry) add(provider *Provider) error {
	byName, ok := r.byKindName[provider.Ref.Kind]
	if !ok {
		byName = make(map[string]*Provider)
		r.byKindName[provider.Ref.Kind] = byName
	}

	if existing, ok := byName[provider.Ref.Name]; ok {
		return fmt.Errorf(
			"%w: providers %s/%s registered by both plugin %#q (%s) and plugin %#q (%s); "+
				"distinct providers must use distinct names within a kind",
			ErrProviderCollision,
			provider.Ref.Kind, provider.Ref.Name,
			existing.Plugin.Name(), existing.Plugin.Path(),
			provider.Plugin.Name(), provider.Plugin.Path())
	}

	byName[provider.Ref.Name] = provider

	return nil
}

// Lookup returns the provider registered for (kind, name), or
// [ErrProviderNotFound] (wrapped with helpful context) if no such
// provider exists.
func (r *Registry) Lookup(kind, name string) (*Provider, error) {
	byName, kindExists := r.byKindName[kind]
	if !kindExists {
		return nil, fmt.Errorf("%w: no providers of kind %#q have been registered",
			ErrProviderNotFound, kind)
	}

	provider, providerExists := byName[name]
	if !providerExists {
		available := make([]string, 0, len(byName))
		for availableName := range byName {
			available = append(available, availableName)
		}

		slices.Sort(available)

		return nil, fmt.Errorf(
			"%w: no provider %s/%s registered; available %s providers: %s",
			ErrProviderNotFound, kind, name, kind, strings.Join(available, ", "))
	}

	return provider, nil
}

// ListByKind returns all providers registered under kind, sorted by
// name. Returns a nil slice when no providers of that kind exist.
func (r *Registry) ListByKind(kind string) []*Provider {
	byName, ok := r.byKindName[kind]
	if !ok {
		return nil
	}

	out := make([]*Provider, 0, len(byName))
	for _, provider := range byName {
		out = append(out, provider)
	}

	slices.SortFunc(out, func(left, right *Provider) int {
		return strings.Compare(left.Ref.Name, right.Ref.Name)
	})

	return out
}

// All returns every registered provider in deterministic order
// (sorted by kind, then name).
func (r *Registry) All() []*Provider {
	kinds := make([]string, 0, len(r.byKindName))
	for kind := range r.byKindName {
		kinds = append(kinds, kind)
	}

	slices.Sort(kinds)

	var out []*Provider
	for _, kind := range kinds {
		out = append(out, r.ListByKind(kind)...)
	}

	return out
}

// Kinds returns all kinds for which at least one provider is registered,
// in deterministic (sorted) order.
func (r *Registry) Kinds() []string {
	kinds := make([]string, 0, len(r.byKindName))
	for kind := range r.byKindName {
		kinds = append(kinds, kind)
	}

	slices.Sort(kinds)

	return kinds
}

// lookupToolByName picks a tool out of plugin.Tools() by name. Returns
// ok=false when no such tool exists.
func lookupToolByName(plugin *Plugin, name string) (mcp.Tool, bool) {
	for _, tool := range plugin.Tools() {
		if tool.Name == name {
			return tool, true
		}
	}

	return mcp.Tool{}, false
}
