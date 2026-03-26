// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package cargovendor implements a [sourcegen.SourceGenerator] that produces
// vendored Cargo dependency archives using rust2rpm.
package cargovendor

import (
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/workdir"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/providers/sourcegen"
	"github.com/microsoft/azure-linux-dev-tools/internal/rpm"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/defers"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
)

const rust2rpmProgram = "rust2rpm"

func rust2rpmPrereq() *prereqs.PackagePrereq {
	return &prereqs.PackagePrereq{
		FedoraPackages:     []string{"rust2rpm"},
		AzureLinuxPackages: []string{"rust2rpm"},
	}
}

// Generator produces vendored Cargo dependency archives by invoking rust2rpm.
type Generator struct {
	env *azldev.Env
}

// Compile-time check that [Generator] implements [sourcegen.SourceGenerator].
var _ sourcegen.SourceGenerator = (*Generator)(nil)

// New creates a new cargo-vendor [Generator].
func New(env *azldev.Env) *Generator {
	return &Generator{env: env}
}

// CheckPrerequisites verifies that rust2rpm is available on the host.
func (g *Generator) CheckPrerequisites(ctx opctx.Ctx) error {
	err := prereqs.RequireExecutable(ctx, rust2rpmProgram, rust2rpmPrereq())
	if err != nil {
		return fmt.Errorf("prerequisite check for %#q failed:\n%w", rust2rpmProgram, err)
	}

	return nil
}

// Generate produces a vendored Cargo dependency tarball for the component
// described by request.
func (g *Generator) Generate(ctx opctx.Ctx, request *sourcegen.GenerateRequest) (err error) {
	crateName := ResolveCrateName(request)
	slog.Info("Resolving crate version...", "crate", crateName, "component", request.Component.GetName())

	version, err := g.resolveVersion(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to resolve crate version for %#q:\n%w", crateName, err)
	}

	slog.Info("Generating vendored cargo dependencies...",
		"crate", crateName,
		"version", version,
		"destination", request.DestPath)

	// Create a temporary directory for rust2rpm output.
	tempDir, err := fileutils.MkdirTemp(g.env.FS(), request.SourcesDir, "cargo-vendor-")
	if err != nil {
		return fmt.Errorf("failed to create temp dir for cargo-vendor:\n%w", err)
	}

	defer fileutils.RemoveAllAndUpdateErrorIfNil(g.env.FS(), tempDir, &err)

	// Run rust2rpm to generate the vendor tarball.
	err = g.runRust2RPM(ctx, crateName, version, tempDir)
	if err != nil {
		return fmt.Errorf("rust2rpm failed for %#q:\n%w", crateName, err)
	}

	// Find the generated tarball and move it to the destination.
	err = g.moveOutputToDestination(tempDir, request.DestPath)
	if err != nil {
		return err
	}

	slog.Info("Cargo vendor tarball generated successfully",
		"crate", crateName,
		"version", version,
		"output", request.DestPath)

	return nil
}

// ResolveCrateName determines the crate name with priority:
// (1) explicit CrateName property, (2) component name with "rust-" prefix removed,
// (3) component name as-is.
func ResolveCrateName(request *sourcegen.GenerateRequest) string {
	if request.Origin.CrateName != "" {
		return request.Origin.CrateName
	}

	name := request.Component.GetName()
	if trimmed := strings.TrimPrefix(name, "rust-"); trimmed != name {
		return trimmed
	}

	return name
}

// resolveVersion determines the crate version, either from an explicit override
// or by parsing the component's spec file.
func (g *Generator) resolveVersion(
	ctx opctx.Ctx, request *sourcegen.GenerateRequest,
) (string, error) {
	if request.Origin.Version != "" {
		slog.Debug("Using explicit version override", "version", request.Origin.Version)

		return request.Origin.Version, nil
	}

	return g.extractVersionFromSpec(ctx, request)
}

// extractVersionFromSpec creates a temporary build environment to parse the
// component's spec file and extract the Version tag.
func (g *Generator) extractVersionFromSpec(
	ctx opctx.Ctx, request *sourcegen.GenerateRequest,
) (version string, err error) {
	componentConfig := request.Component.GetConfig()

	specPath := filepath.Join(request.SourcesDir, componentConfig.Name+".spec")

	exists, err := fileutils.Exists(g.env.FS(), specPath)
	if err != nil {
		return "", fmt.Errorf("failed to check for spec file %#q:\n%w", specPath, err)
	}

	if !exists {
		return "", fmt.Errorf(
			"spec file %#q not found; either fetch the spec first or"+
				" set an explicit version in the origin config", specPath)
	}

	workDirFactory, err := workdir.NewFactory(g.env.FS(), g.env.WorkDir(), g.env.ConstructionTime())
	if err != nil {
		return "", fmt.Errorf("failed to create work dir factory:\n%w", err)
	}

	buildEnv, err := workdir.MkComponentBuildEnvironment(g.env, workDirFactory, componentConfig, "cargo-vendor", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create build environment for version extraction:\n%w", err)
	}

	defer defers.HandleDeferError(func() error { return buildEnv.Destroy(g.env) }, &err)

	buildOptions := rpm.BuildOptions{
		With:    componentConfig.Build.With,
		Without: componentConfig.Build.Without,
		Defines: componentConfig.Build.Defines,
	}

	specInfo, err := rpm.NewSpecQuerier(buildEnv, buildOptions).QuerySpec(ctx, specPath)
	if err != nil {
		return "", fmt.Errorf("failed to query spec %#q for version:\n%w", specPath, err)
	}

	version = specInfo.Version.Version()
	if version == "" {
		return "", fmt.Errorf("spec %#q has an empty Version tag", specPath)
	}

	slog.Debug("Extracted version from spec", "version", version, "spec", specPath)

	return version, nil
}

// runRust2RPM invokes rust2rpm to generate the vendor tarball.
func (g *Generator) runRust2RPM(ctx opctx.Ctx, crateName, version, outputDir string) error {
	crateSpec := crateName + "@" + version

	cmd, err := g.env.Command(exec.CommandContext(ctx, rust2rpmProgram, "-V", "auto", crateSpec, "--no-existence-check", "-o", outputDir))
	if err != nil {
		return fmt.Errorf("failed to create rust2rpm command:\n%w", err)
	}

	cmd.SetLongRunning("Generating vendor tarball...")

	err = cmd.Run(ctx)
	if err != nil {
		return fmt.Errorf("failed to run %#q:\n%w",
			strings.Join([]string{rust2rpmProgram, "-V", "auto", crateSpec, "-o", outputDir}, " "), err)
	}

	return nil
}

// moveOutputToDestination finds the single .tar.xz file in outputDir
// and moves it to destPath.
func (g *Generator) moveOutputToDestination(outputDir, destPath string) error {
	matches, err := fileutils.Glob(g.env.FS(), filepath.Join(outputDir, "*.tar.xz"))
	if err != nil {
		return fmt.Errorf("failed to search for generated tarball in %#q:\n%w", outputDir, err)
	}

	if len(matches) == 0 {
		return fmt.Errorf("rust2rpm did not produce any .tar.xz files in %#q", outputDir)
	}

	if len(matches) > 1 {
		return fmt.Errorf("rust2rpm produced multiple .tar.xz files in %#q: %v", outputDir, matches)
	}

	err = g.env.FS().Rename(matches[0], destPath)
	if err != nil {
		return fmt.Errorf("failed to move generated tarball from %#q to %#q:\n%w",
			matches[0], destPath, err)
	}

	return nil
}
