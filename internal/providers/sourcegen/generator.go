// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package sourcegen provides an extensible framework for generating source files
// on behalf of components. Each generator type (e.g., cargo-vendor) implements
// the [SourceGenerator] interface.
package sourcegen

import (
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
)

// SourceGenerator defines the interface for a source file generator.
// Implementations produce source artifacts (e.g., vendored dependency archives)
// that are required to build a component but are not available from upstream.
type SourceGenerator interface {
	// CheckPrerequisites verifies that all external tools required by this
	// generator are available. It may offer to auto-install missing tools
	// depending on the policy configured in ctx.
	CheckPrerequisites(ctx opctx.Ctx) error

	// Generate produces the source file described by request, writing the
	// result to [GenerateRequest.DestPath].
	Generate(ctx opctx.Ctx, request *GenerateRequest) error
}
