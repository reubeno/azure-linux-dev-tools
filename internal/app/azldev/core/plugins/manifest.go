// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// ManifestResourceURI is the well-known URI a plugin can publish to expose
// cross-cutting metadata to azldev. Per-tool placement metadata lives in the
// individual tool's '_meta.azldev' field, not here; the manifest resource is
// reserved for plugin-wide concerns.
const ManifestResourceURI = "azldev://manifest"

// SupportedManifestProtocolVersion is the highest manifest protocol-version
// azldev knows how to interpret. A plugin reporting a higher major value is
// rejected with a clear upgrade hint.
const SupportedManifestProtocolVersion = 1

// MetaKey is the key azldev uses inside MCP's vendor-extension '_meta' object
// (both at the plugin manifest level and on individual tools). All
// azldev-specific metadata lives under this single key so it can never
// collide with other vendors using the same '_meta' channel.
const MetaKey = "azldev"

// ErrManifestVersion is returned when a plugin advertises a manifest with a
// protocol-version azldev does not yet support. The error message includes
// the version reported and the highest version azldev recognizes.
var ErrManifestVersion = errors.New("unsupported plugin manifest protocol version")

// ErrManifestMalformed is returned when a manifest resource is present but
// fails to parse, has the wrong shape, or fails internal validation.
var ErrManifestMalformed = errors.New("malformed plugin manifest")

// Manifest is the parsed view of the azldev://manifest MCP resource. It
// captures plugin-level metadata that is independent of any specific tool.
//
// Only the fields relevant to Phases 2–3 are populated at this time. Later
// phases (settings schema, …) will extend this struct without breaking
// existing consumers.
//
// JSON tags use kebab-case to match the on-the-wire manifest contract
// documented in 'docs/user/explanation/plugins.md'.
type Manifest struct {
	// ProtocolVersion is the manifest schema version the plugin claims to
	// implement. Must be > 0 and <= [SupportedManifestProtocolVersion].
	ProtocolVersion int `json:"protocol-version"` //nolint:tagliatelle // wire-format kebab-case is the contract.

	// Title overrides the human-readable display title for the plugin in
	// 'azldev plugin --help' (and similar surfaces). Optional.
	Title string `json:"title,omitempty"`

	// Description overrides the long-form plugin description shown by
	// 'azldev advanced plugin info'. Optional.
	Description string `json:"description,omitempty"`

	// Providers is the list of named provider implementations the plugin
	// registers. Each entry binds a contract identifier (kind+name) to a
	// tool the plugin advertises. See [ProviderRef] for field details and
	// [Registry] for how azldev consumes them.
	Providers []ProviderRef `json:"providers,omitempty"`
}

// ToolGraftHints is the parsed view of a tool's '_meta.azldev' object. It
// instructs azldev where to place the tool in the Cobra hierarchy and how
// to surface it in help. All fields are optional; a nil [ToolGraftHints]
// means "use the default fallback namespace".
//
// JSON tags use kebab-case to match the on-the-wire contract documented
// in 'docs/user/explanation/plugins.md'.
type ToolGraftHints struct {
	// CommandPath, if non-empty, names the Cobra command-path at which the
	// tool should be grafted. The first N-1 segments are group nodes (which
	// may already exist as built-ins or be introduced by this or another
	// plugin); the final segment is the leaf command name. An empty list
	// means "use the fallback namespace".
	CommandPath []string `json:"command-path,omitempty"` //nolint:tagliatelle // wire-format kebab-case.

	// Aliases are additional names by which the tool can be invoked at its
	// graft path. Ignored when the tool falls back to the namespace.
	Aliases []string `json:"aliases,omitempty"`

	// Hidden, when true, hides the tool from default help output (it is
	// still invokable). Useful for utility/diagnostic tools.
	Hidden bool `json:"hidden,omitempty"`

	// GroupTitle is used only when [CommandPath] introduces a brand-new
	// top-level group node. The title is consulted only on the segment
	// being newly created; subsequent plugins contributing to the same
	// group inherit it silently.
	GroupTitle string `json:"group-title,omitempty"` //nolint:tagliatelle // wire-format kebab-case.
}

// readManifest fetches the [ManifestResourceURI] resource from a freshly
// initialized plugin client and decodes it. A missing resource is treated
// as benign (returns nil, nil) so plugins remain free to skip the manifest
// when they don't need to exercise Phase 2+ features.
//
// A present-but-malformed manifest, or one that advertises an unsupported
// protocol version, is returned as a hard error so the user notices.
//
//nolint:nilnil // (nil, nil) is the documented "no manifest, no error" signal.
func readManifest(ctx context.Context, mcpClient *client.Client) (*Manifest, error) {
	caps := mcpClient.GetServerCapabilities()
	if caps.Resources == nil {
		// Plugin doesn't advertise resource support; treat as no manifest.
		return nil, nil
	}

	req := mcp.ReadResourceRequest{}
	req.Params.URI = ManifestResourceURI

	result, err := mcpClient.ReadResource(ctx, req)
	if err != nil {
		// Most plugins won't publish a manifest. Treat any read error as
		// "no manifest" so we don't block on optional metadata. The plugin
		// is still required to behave correctly without one.
		slog.Debug("manifest resource not available; using defaults",
			"uri", ManifestResourceURI, "err", err)

		return nil, nil
	}

	text, err := manifestText(result.Contents)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrManifestMalformed, err)
	}

	var manifest Manifest
	if err := json.Unmarshal([]byte(text), &manifest); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON in manifest:\n%w", ErrManifestMalformed, err)
	}

	if err := validateManifest(&manifest); err != nil {
		return nil, err
	}

	return &manifest, nil
}

// manifestText extracts the textual body of the (single) text resource
// content entry returned by reads/resources. We deliberately reject empty,
// multi-entry, and binary-blob shapes since the manifest is conceptually
// one JSON document.
func manifestText(contents []mcp.ResourceContents) (string, error) {
	if len(contents) == 0 {
		return "", errors.New("manifest resource returned no content")
	}

	if len(contents) > 1 {
		return "", fmt.Errorf("manifest resource returned %d content entries; expected exactly 1", len(contents))
	}

	text, isText := contents[0].(mcp.TextResourceContents)
	if !isText {
		return "", errors.New("manifest resource content is not textual")
	}

	return text.Text, nil
}

// validateManifest applies cheap structural checks. The version check is
// the only one that's strictly required for forward compatibility; the
// others are courtesy validation so misuse is caught early.
func validateManifest(manifest *Manifest) error {
	if manifest.ProtocolVersion < 1 {
		return fmt.Errorf(
			"%w: 'protocol-version' must be >= 1, got %d",
			ErrManifestMalformed, manifest.ProtocolVersion)
	}

	if manifest.ProtocolVersion > SupportedManifestProtocolVersion {
		return fmt.Errorf(
			"%w: plugin advertises protocol-version %d but this azldev only supports up to %d; "+
				"either upgrade azldev or downgrade the plugin",
			ErrManifestVersion, manifest.ProtocolVersion, SupportedManifestProtocolVersion)
	}

	return nil
}

// ParseToolGraftHints extracts the per-tool '_meta.azldev' sub-object and
// returns it as a typed [ToolGraftHints]. Returns (nil, nil) when no hints
// were provided, or (nil, error) when the hints block is present but
// malformed (in which case the caller should warn and fall back to the
// namespace placement).
func ParseToolGraftHints(tool mcp.Tool) (*ToolGraftHints, error) {
	return parseToolGraftHints(tool)
}

// parseToolGraftHints is the internal implementation. Exposed externally
// via [ParseToolGraftHints] so admin commands and other callers can
// re-derive a tool's intended placement without reaching into unexported
// helpers.
//
//nolint:nilnil // (nil, nil) is the documented "no hints, no error" signal.
func parseToolGraftHints(tool mcp.Tool) (*ToolGraftHints, error) {
	if tool.Meta == nil || tool.Meta.AdditionalFields == nil {
		return nil, nil
	}

	raw, ok := tool.Meta.AdditionalFields[MetaKey]
	if !ok {
		return nil, nil
	}

	// Round-trip via JSON to honor json tags rather than hand-coding the
	// type-assertion ladder for every field.
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to re-encode tool _meta.azldev:\n%w",
			ErrManifestMalformed, err)
	}

	var hints ToolGraftHints
	if err := json.Unmarshal(encoded, &hints); err != nil {
		return nil, fmt.Errorf("%w: invalid tool _meta.azldev shape:\n%w",
			ErrManifestMalformed, err)
	}

	return &hints, nil
}
