// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/artifacts"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

func executionStrategy(test projectconfig.ResolvedTest, override string) string {
	if override != "" {
		return override
	}

	if test.Definition.Execution.Strategy != "" {
		return test.Definition.Execution.Strategy
	}

	if test.Definition.Type == "pytest" {
		return "host"
	}

	return "vm"
}

func selectBaseName(subject Subject, test projectconfig.ResolvedTest, requirements testframework.Requirements,
	options Options, strategy string,
) (string, error) {
	if subject.Kind != componentSubject {
		return "", nil
	}

	if !requirements.ImageRequired {
		if options.BaseImage != "" || options.BaseImagePath != "" ||
			options.RebuildBaseImage || options.BaseImageSource != "" {
			return "", errors.New("base image options do not apply to this execution strategy")
		}

		return "", nil
	}

	baseName := options.BaseImage
	if baseName == "" {
		baseName = test.Definition.Execution.BaseImages[strategy]
	}

	if baseName == "" {
		baseName = subject.Distro.TestBaseImages[strategy]
	}

	if baseName == "" && options.BaseImagePath != "" {
		baseName = "explicit-base"
	}

	if baseName == "" {
		return "", errors.New("no named base image configured; set '--base-image' or '--base-image-path'")
	}

	if options.baseSelection().Source == artifacts.SourceLKG {
		for _, variant := range subject.Distro.Images[baseName].Architectures[options.Arch] {
			if slices.Contains(requirements.ImageFormats, variant.Format) {
				return baseName, nil
			}
		}

		return "", fmt.Errorf("no compatible published base image %#q for architecture %#q", baseName, options.Arch)
	}

	return baseName, nil
}

func saveResults(env *azldev.Env, runDir, junitPath string, results []testframework.RunResult) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("encode test results:\n%w", err)
	}

	path := filepath.Join(runDir, "results.json")
	if err := fileutils.WriteFile(env.FS(), path, data, fileperms.PrivateFile); err != nil {
		return fmt.Errorf("write test results:\n%w", err)
	}

	if junitPath != "" {
		return writeJUnit(env.FS(), junitPath, results)
	}

	return nil
}
