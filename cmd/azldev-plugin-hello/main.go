// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The azldev-plugin-hello binary is a tiny reference implementation of an
// azldev plugin. It speaks MCP over stdio and advertises a single tool,
// 'greet', that returns a greeting string.
//
// Its primary role is to anchor scenario tests for the plugin loader.
// External plugin authors may use it as a starting template, but production
// plugins should use the (forthcoming) plugin SDK package once available.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	srv := server.NewMCPServer("hello", "1.0.0",
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	greetTool := mcp.NewTool("greet",
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

	srv.AddTool(greetTool, greetHandler)

	if err := server.ServeStdio(srv); err != nil {
		log.Fatalf("plugin failed: %v", err)
	}
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
