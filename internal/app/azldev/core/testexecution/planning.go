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

	if test.Definition.Type == pytestFramework {
		return "host"
	}

	return "vm"
}

func validateOverrides(tests []projectconfig.ResolvedTest, options Options) error {
	frameworks := make(map[string]bool)
	strategies := make(map[string]bool)

	for _, test := range tests {
		frameworks[test.Definition.Type] = true
		strategies[executionStrategy(test, options.Strategy)] = true
	}

	if options.LisaDir != "" && !frameworks[lisaFramework] {
		return errors.New("'--lisa-dir' requires a selected LISA test")
	}

	if options.Plan != "" && !frameworks[tmtFramework] {
		return errors.New("'--plan' requires a selected TMT test")
	}

	if options.SourceDir != "" && !frameworks[tmtFramework] && !frameworks[pytestFramework] {
		return errors.New("'--source-dir' requires a selected TMT or pytest test")
	}

	if !strategies["ssh"] && options.hasSSHOptions() {
		return errors.New("SSH options require a selected test using execution strategy 'ssh'")
	}

	return nil
}

func executionFor(test projectconfig.ResolvedTest, options Options, strategy string) testframework.ExecutionSpec {
	execution := options.execution(testframework.Strategy(strategy))
	if test.Definition.Type != lisaFramework {
		execution.FrameworkDir = ""
	} else {
		execution.SourceDir = ""
	}

	if test.Definition.Type != tmtFramework {
		execution.Plan = ""
	}

	if execution.Strategy != testframework.SSH {
		execution.SSHHost, execution.SSHUser, execution.SSHKey = "", "", ""
		execution.SSHPort = 0
	}

	return execution
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
