// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package testcmd exposes the shared test catalog and subject-oriented execution.
package testcmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/cmds/component"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/cmds/image"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testexecution"
	"github.com/spf13/cobra"
)

// OnAppInit registers the top-level test command family.
func OnAppInit(app *azldev.App) {
	app.AddTopLevelCommand(NewTestCmd())
}

// NewTestCmd constructs the shared test command family.
func NewTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "test", Short: "List configured tests and run them against build artifacts",
		Long: `Inspect the shared test catalog or run LISA, TMT, and pytest tests against
a named component or image. Listing reads configuration only; it does not
discover individual framework cases, acquire artifacts, or execute test code.`,
	}
	cmd.AddCommand(NewRunCmd(), NewListCmd())

	return cmd
}

// NewRunCmd constructs framework-independent test execution for either subject kind.
func NewRunCmd() *cobra.Command {
	var componentName, imageName string

	options := &testexecution.Options{}
	cmd := &cobra.Command{
		Use: "run", Short: "Run tests against a component or image",
		Long: `Run associated tests, or explicit --test selections, against exactly one
--component or --image. Selectors accept test names and test-group names.

Reuse local subject artifacts, building when missing; --rebuild forces a build,
--artifact-source lkg selects published artifacts, and --rpm/--image-path supply
explicit component/image inputs. Component supporting images default independently
to published LKG; use --base-image-source local or --base-image-path to override.
Component selection does not require packaging lock consistency.

TMT supports VM, container, SSH, and host component tests; image tests support VM
and container. LISA uses VMs, and pytest inspects artifacts on the host. Host/SSH
execution may modify the target: use designated disposable test systems.

Results and native logs remain under the test work directory. --junit-xml writes
an aggregate report. All selected tests run unless --fail-fast is set. The older
'component test NAME' and 'image test NAME' commands remain supported.`,
		Example: `  azldev test run --component curl --base-image vm-base
  azldev test run --component curl --rpm ./curl.rpm --base-image-path ./base.qcow2
  azldev test run --image vm-base --image-path ./candidate.qcow2 --test smoke
  azldev test run --image vm-base --artifact-source lkg`,
		Args: cobra.NoArgs,
		RunE: azldev.RunFunc(func(env *azldev.Env) (interface{}, error) {
			if (componentName == "") == (imageName == "") {
				return nil, errors.New("select exactly one non-empty '--component' or '--image'")
			}

			if componentName != "" {
				if options.ImagePath != "" {
					return nil, errors.New("'--image-path' requires '--image'; use '--base-image-path' for component tests")
				}

				return component.RunComponentTests(env, componentName, *options, false)
			}

			if len(options.RPMPaths) != 0 {
				return nil, errors.New("'--rpm' requires '--component'")
			}

			return image.RunImageTests(env, imageName, *options)
		}),
	}
	registerSubjectFlags(cmd, &componentName, &imageName)
	cmd.MarkFlagsOneRequired("component", "image")
	testexecution.RegisterRunFlags(cmd, options)

	return cmd
}

// NewListCmd lists configured tests without framework discovery or artifact preparation.
func NewListCmd() *cobra.Command {
	var (
		componentName, imageName string
		selectors                []string
	)

	cmd := &cobra.Command{
		Use: "list", Short: "List configured tests, optionally associated with a component or image",
		Long: `List all named tests in the project catalog, or the tests associated with
--component or --image. Expand groups and deduplicate test names. Explicit --test
selectors replace the associations, just as for 'test run'.

This lists configured framework invocations, not individual native cases.
No test sources are fetched, Python environments installed, artifacts built or
downloaded, or framework code executed. Packaging locks are not validated.
A subject with no associated tests produces an empty list. Use -O json for
structured output.`,
		Example: `  azldev test list
  azldev test list --component curl
  azldev test list --image vm-base --test smoke
  azldev test list --test regression -O json`,
		Args: cobra.NoArgs,
		RunE: azldev.RunFunc(func(env *azldev.Env) (interface{}, error) {
			return testexecution.List(env, componentName, imageName, selectors)
		}),
	}
	registerSubjectFlags(cmd, &componentName, &imageName)
	cmd.Flags().StringSliceVarP(&selectors, "test", "t", nil, "Test or test-group name (repeatable)")
	azldev.ExportAsReadOnlyMCPTool(cmd)

	return cmd
}

func registerSubjectFlags(cmd *cobra.Command, componentName, imageName *string) {
	cmd.Flags().StringVar(componentName, "component", "", "Component name")
	cmd.Flags().StringVar(imageName, "image", "", "Image name (project definition or published catalog)")
	cmd.MarkFlagsMutuallyExclusive("component", "image")
	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		for name, value := range map[string]string{"component": *componentName, "image": *imageName} {
			if cmd.Flags().Changed(name) && strings.TrimSpace(value) == "" {
				return fmt.Errorf("'--%s' must not be empty", name)
			}
		}

		return nil
	}
}
