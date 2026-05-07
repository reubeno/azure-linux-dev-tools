// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The azldev-plugin-hello binary is a tiny reference implementation of an
// azldev plugin. It speaks MCP over stdio and advertises:
//
//   - a 'greet' tool placed in the default fallback namespace
//     ('azldev plugin hello greet').
//   - a 'cloud-greet' tool that uses the azldev manifest extension to graft
//     itself into the existing 'component' command group as
//     'azldev component cloud-greet'.
//   - the 'azldev://manifest' resource describing the plugin to azldev.
//
// Its primary role is to anchor scenario tests for the plugin loader.
// External plugin authors may use it as a starting template, but production
// plugins should use the (forthcoming) plugin SDK package once available.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const manifestURI = "azldev://manifest"

// manifestJSON is the body of the azldev://manifest resource. The shape is
// the public Phase 2 contract.
const manifestJSON = `{
  "protocol-version": 1,
  "title": "Hello plugin (reference)",
  "description": "A tiny reference plugin used by azldev's scenario tests."
}`

func main() {
	srv := server.NewMCPServer("hello", "1.0.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(false, false),
		server.WithRecovery(),
	)

	srv.AddTool(makeGreetTool(), greetHandler)
	srv.AddTool(makeCloudGreetTool(), greetHandler)

	srv.AddResource(
		mcp.NewResource(manifestURI, "azldev plugin manifest",
			mcp.WithMIMEType("application/json"),
			mcp.WithResourceDescription("Plugin metadata consumed by azldev.")),
		manifestHandler,
	)

	if err := server.ServeStdio(srv); err != nil {
		log.Fatalf("plugin failed: %v", err)
	}
}

// makeGreetTool returns the namespace-only greet tool. It deliberately
// omits the '_meta.azldev.command-path' hint so it lands in the fallback
// 'azldev plugin hello greet' namespace.
func makeGreetTool() mcp.Tool {
	return mcp.NewTool("greet",
		mcp.WithDescription("Greet someone by name."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The name of the person to greet."),
		),
		mcp.WithString("salutation",
			mcp.DefaultString("hello"),
			mcp.Description("The salutation to use (e.g. 'hello', 'hi', 'howdy')."),
		),
	)
}

// makeCloudGreetTool returns a copy of the greet tool but tagged with an
// '_meta.azldev.command-path' hint that grafts it under the existing
// 'component' command group.
func makeCloudGreetTool() mcp.Tool {
	tool := mcp.NewTool("cloud-greet",
		mcp.WithDescription("Greet someone by name (grafted under 'component')."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The name of the person to greet."),
		),
		mcp.WithString("salutation",
			mcp.DefaultString("hello"),
			mcp.Description("The salutation to use (e.g. 'hello', 'hi', 'howdy')."),
		),
	)

	tool.Meta = mcp.NewMetaFromMap(map[string]any{
		"azldev": map[string]any{
			"command-path": []any{"component", "cloud-greet"},
		},
	})

	return tool
}

func greetHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	name, _ := args["name"].(string)
	if name == "" {
		return mcp.NewToolResultError("the 'name' argument is required and must be a non-empty string"), nil
	}

	salutation, _ := args["salutation"].(string)
	if salutation == "" {
		salutation = "hello"
	}

	return mcp.NewToolResultText(fmt.Sprintf("%s, %s!", salutation, name)), nil
}

func manifestHandler(_ context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	// Validate the manifest body parses as JSON at startup time so a typo
	// here doesn't slip into production. The check is a no-op at runtime
	// once the binary builds cleanly.
	var sanity any
	if err := json.Unmarshal([]byte(manifestJSON), &sanity); err != nil {
		return nil, fmt.Errorf("internal: malformed manifest:\n%w", err)
	}

	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      manifestURI,
			MIMEType: "application/json",
			Text:     manifestJSON,
		},
	}, nil
}
