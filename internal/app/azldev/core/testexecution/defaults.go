// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/tmt"
)

func applyImageDefaults(cfg *projectconfig.ProjectConfig, subject Subject, plan *plannedTest, options Options) error {
	if plan.test.Definition.Type != tmtFramework || plan.execution.Strategy != testframework.VM {
		return nil
	}

	name := subject.Name
	if subject.Kind == componentSubject {
		name = plan.baseName
	}

	const field = "cloud-init-runcmds"
	if _, specified := plan.test.Definition.Tmt[field]; specified {
		plan.bootstrapSource = "test:" + plan.test.Name
	} else {
		commands, origin := imageBootstrap(cfg, subject, name, options)
		if commands == nil {
			return nil
		}

		plan.bootstrapSource = origin

		effective := make(map[string]any, len(plan.test.Definition.Tmt)+1)
		maps.Copy(effective, plan.test.Definition.Tmt)
		effective[field] = slices.Clone(*commands)
		plan.test.Definition.Tmt = effective

		runner, err := tmt.New(plan.test.Name, plan.test.Definition.Tmt)
		if err != nil {
			return fmt.Errorf("configure inherited TMT bootstrap:\n%w", err)
		}

		plan.runner = runner
	}

	slog.Info("Using TMT guest bootstrap configuration", "test", plan.test.Name,
		"image", name, "defaults-origin", plan.bootstrapSource)

	return nil
}

func imageBootstrap(cfg *projectconfig.ProjectConfig, subject Subject, name string,
	options Options,
) (commands *[]string, origin string) {
	// An anonymous file is not the image that test/distro defaults would otherwise select.
	if subject.Kind == componentSubject && options.BaseImagePath != "" && options.BaseImage == "" {
		return nil, ""
	}

	if defaults := subject.Distro.Images[name].TestDefaults; defaults != nil {
		commands = defaults.TMT.CloudInitRuncmds
		if commands != nil {
			origin = "distro-image:" + name
		}
	}

	if cfg != nil {
		if defaults := cfg.Images[name].TestDefaults; defaults != nil && defaults.TMT.CloudInitRuncmds != nil {
			commands = defaults.TMT.CloudInitRuncmds
			origin = "project-image:" + name
		}
	}

	return commands, origin
}
