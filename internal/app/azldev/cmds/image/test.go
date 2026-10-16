// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"fmt"
	"runtime"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/artifacts"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/imagebuilder"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testexecution"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/qemu"
	"github.com/spf13/cobra"
)

func testOnAppInit(_ *azldev.App, parentCmd *cobra.Command) {
	parentCmd.AddCommand(NewImageTestCmd())
}

func NewImageTestCmd() *cobra.Command {
	options := &testexecution.Options{}
	cmd := &cobra.Command{
		Use:   "test IMAGE_NAME",
		Short: "Run tests against an Azure Linux image",
		Long: `Run associated LISA, TMT, or pytest tests against an image.

By default, reuse a local build of the image, or build it if missing. Artifact
age is reported without claiming freshness. Use --rebuild to force a local
build, --artifact-source lkg to fetch the distro version's published image,
or --image-path to use an explicit artifact.

Tests and groups are declared in [tests] and [test-groups] and referenced by
[images.NAME.tests]. Use --test to select explicit test/group names. The shared
runner selects an image format compatible with every selected test.

LISA runs live VM tests. TMT supports VM and container image tests. Pytest
runs on the host and can inspect the image via {image-path} or the artifact
manifest exposed through AZLDEV_TEST_INPUTS and {inputs-path}. The host running
the framework is distinct from the environment under test.

All selected tests run by default; --fail-fast stops after a failure.
Native reports and structured results are retained in isolated run directories.
--junit-xml writes an aggregated report without overwriting individual suites.`,
		Example: `  azldev image test vm-base
  azldev image test vm-base --rebuild
  azldev image test vm-base --artifact-source lkg
  azldev image test vm-base --image-path ./out/image.qcow2 --test smoke
  azldev image test container-base --execution container`,
		Args: cobra.ExactArgs(1),
		RunE: azldev.RunFuncWithExtraArgs(func(env *azldev.Env, args []string) (interface{}, error) {
			return RunImageTests(env, args[0], *options)
		}),
		ValidArgsFunction: generateImageNameCompletions,
	}
	testexecution.RegisterFlags(cmd, options, false)

	return cmd
}

// RunImageTests runs an image's selected tests using the shared artifact and framework services.
func RunImageTests(env *azldev.Env, name string, options testexecution.Options) ([]testframework.RunResult, error) {
	_, distro, err := env.Distro()
	if err != nil {
		return nil, fmt.Errorf("run image tests:\n%w", err)
	}

	image, err := testexecution.FindImage(env, name)
	if err != nil {
		return nil, fmt.Errorf("run image tests:\n%w", err)
	}

	arch := options.Arch
	if arch == "" {
		arch = qemu.GoArchToQEMUArch(runtime.GOARCH)
	}

	if !image.SupportsArchitecture(arch) {
		return nil, fmt.Errorf("image %#q does not support architecture %#q", name, arch)
	}

	associated, err := env.Config().ResolveImageTests(&image)
	if err != nil {
		return nil, fmt.Errorf("run image tests:\n%w", err)
	}

	tests, err := testexecution.Select(env.Config(), associated, options.Tests)
	if err != nil {
		return nil, fmt.Errorf("run image tests:\n%w", err)
	}

	resolver := &artifacts.Resolver{Env: env, BuildImage: imagebuilder.ArtifactBuilder(env)}

	results, err := testexecution.Run(env, testexecution.Subject{
		Name: name, Kind: "image", Distro: distro,
		Capabilities: image.Capabilities.EnabledNames(), Properties: image.Properties,
	}, tests, options, resolver)
	if err != nil {
		return results, fmt.Errorf("run image tests:\n%w", err)
	}

	return results, nil
}
