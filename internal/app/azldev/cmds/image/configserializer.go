// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

// imageTestContext is the JSON structure written to disk and consumed by the Python
// pytest fixtures via the --azldev-config option. It contains the image configuration
// and any additional metadata that tests might need.
type imageTestContext struct {
	// ImageName is the name of the image under test.
	ImageName string `json:"imageName"`

	// ImageDescription is the human-readable description of the image.
	ImageDescription string `json:"imageDescription,omitempty"`

	// Definition holds the image definition metadata.
	Definition imageDefinitionContext `json:"definition,omitempty"`

	// Tests lists the test suite names associated with this image.
	Tests []string `json:"tests,omitempty"`
}

// imageDefinitionContext is the JSON representation of [projectconfig.ImageDefinition].
type imageDefinitionContext struct {
	Type    string `json:"type,omitempty"`
	Path    string `json:"path,omitempty"`
	Profile string `json:"profile,omitempty"`
}

// SerializeImageConfigToJSON writes the image configuration to a JSON file in the
// specified directory. Returns the path to the written file.
func SerializeImageConfigToJSON(
	fs opctx.FS, imageConfig *projectconfig.ImageConfig, dir string,
) (string, error) {
	ctx := imageTestContext{
		ImageName:        imageConfig.Name,
		ImageDescription: imageConfig.Description,
		Definition: imageDefinitionContext{
			Type:    string(imageConfig.Definition.DefinitionType),
			Path:    imageConfig.Definition.Path,
			Profile: imageConfig.Definition.Profile,
		},
		Tests: imageConfig.Tests,
	}

	jsonBytes, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to serialize image config to JSON:\n%w", err)
	}

	outputPath := filepath.Join(dir, "azldev-image-config.json")

	err = fileutils.WriteFile(fs, outputPath, jsonBytes, fileperms.PublicFile)
	if err != nil {
		return "", fmt.Errorf("failed to write image config JSON to %#q:\n%w", outputPath, err)
	}

	return outputPath, nil
}
