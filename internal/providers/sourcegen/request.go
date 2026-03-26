// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sourcegen

import (
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/components"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
)

// GenerateRequest describes a source file to be generated.
type GenerateRequest struct {
	// Component for which the source file is being generated.
	Component components.Component

	// Origin configuration from the component's source-files entry.
	Origin projectconfig.Origin

	// DestPath is the full path where the generated file must be written.
	DestPath string

	// SourcesDir is the directory containing the component's spec file and
	// any other previously-fetched sources. Generators that need to inspect
	// the spec (e.g., to extract Version) should look here.
	SourcesDir string
}
