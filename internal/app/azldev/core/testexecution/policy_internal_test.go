// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/artifacts"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlagsBindEverySharedPolicyField(t *testing.T) {
	var options Options

	cmd := &cobra.Command{}
	RegisterFlags(cmd, &options, true)
	require.NoError(t, cmd.ParseFlags([]string{
		"--test", "one,two", "--execution", "ssh", "--artifact-source", "path", "--rebuild",
		"--arch", "aarch64", "--work-dir", "work", "--junit-xml", "report.xml", "--fail-fast",
		"--source-dir", "source", "--lisa-dir", "lisa", "--plan", "/plan",
		"--ssh-host", "host", "--ssh-user", "user", "--ssh-key", "key", "--ssh-port", "2222",
		"--rpm", "one.rpm,two.rpm", "--base-image", "base", "--base-image-source", "local",
		"--base-image-path", "base.raw", "--rebuild-base-image",
	}))
	assert.Equal(t, Options{
		Tests: []string{"one", "two"}, Strategy: "ssh", ArtifactSource: "path", Rebuild: true,
		Arch: "aarch64", WorkDir: "work", JUnitXMLPath: "report.xml", FailFast: true,
		SourceDir: "source", LisaDir: "lisa", Plan: "/plan",
		SSHHost: "host", SSHUser: "user", SSHKey: "key", SSHPort: 2222,
		RPMPaths: []string{"one.rpm", "two.rpm"}, BaseImage: "base", BaseImageSource: "local",
		BaseImagePath: "base.raw", RebuildBaseImage: true,
	}, options)

	for _, component := range []bool{false, true} {
		options = Options{}
		cmd = &cobra.Command{}
		RegisterFlags(cmd, &options, component)
		require.NoError(t, cmd.ParseFlags([]string{"--image-path", "input.raw", "--test-suite", "legacy"}))
		assert.Equal(t, []string{"legacy"}, options.Tests)

		if component {
			assert.Equal(t, "input.raw", options.BaseImagePath)
			assert.Empty(t, options.ImagePath)
		} else {
			assert.Equal(t, "input.raw", options.ImagePath)
			assert.Empty(t, options.BaseImagePath)
			assert.Nil(t, cmd.Flags().Lookup("base-image"))
			assert.Nil(t, cmd.Flags().Lookup("rpm"))
		}

		for _, name := range []string{"image-path", "ssh-key", "junit-xml", "work-dir", "source-dir", "lisa-dir"} {
			assert.NotEmpty(t, cmd.Flags().Lookup(name).Annotations, name)
		}
	}
}

func TestIndependentSubjectAndBaseSelection(t *testing.T) {
	for _, test := range []struct {
		options Options
		want    artifacts.Selection
	}{
		{Options{}, artifacts.Selection{Source: "lkg"}},
		{Options{RebuildBaseImage: true}, artifacts.Selection{Source: "local", Rebuild: true}},
		{Options{BaseImagePath: "/base"}, artifacts.Selection{Source: "path", Paths: []string{"/base"}}},
		{Options{BaseImageSource: "local"}, artifacts.Selection{Source: "local"}},
		{
			Options{BaseImageSource: "local", BaseImagePath: "/base"},
			artifacts.Selection{Source: "local", Paths: []string{"/base"}},
		},
	} {
		assert.Equal(t, test.want, test.options.baseSelection())
	}

	options := Options{ArtifactSource: "path", ImagePath: "/subject", RPMPaths: []string{"/pkg"}, Rebuild: true}
	assert.Equal(t, artifacts.Selection{Source: "path", Paths: []string{"/pkg"}, Rebuild: true},
		options.subjectSelection(componentSubject))
	assert.Equal(t, artifacts.Selection{Source: "path", Paths: []string{"/subject"}, Rebuild: true},
		options.subjectSelection(imageSubject))
	assert.Equal(t, artifacts.Selection{}, (Options{}).subjectSelection(imageSubject))

	for _, options := range []Options{{SSHHost: "host"}, {SSHUser: "user"}, {SSHKey: "key"}, {SSHPort: 22}} {
		assert.True(t, options.hasSSHOptions())
		require.Error(t, validateOverrides(nil, options))
	}

	assert.False(t, (Options{}).hasSSHOptions())
}

func TestNormalizePathsAndRejectOverlap(t *testing.T) {
	env := testutils.NewTestEnv(t)
	options := Options{WorkDir: "work", SourceDir: "source", LisaDir: "lisa", SSHKey: "key", JUnitXMLPath: "out.xml"}
	require.NoError(t, normalizeOptions(env.Env, &options))
	assert.Equal(t, "/project/work", options.WorkDir)
	assert.Equal(t, "/project/source", options.SourceDir)
	assert.Equal(t, "/project/lisa", options.LisaDir)
	assert.Equal(t, "/project/key", options.SSHKey)
	assert.Equal(t, "/project/out.xml", options.JUnitXMLPath)

	for _, source := range []string{"/project/work", "/project/work/nested", "/project"} {
		options := Options{WorkDir: "/project/work", SourceDir: source}
		require.ErrorContains(t, normalizeOptions(env.Env, &options), "overlap")
	}

	assert.False(t, containsDir("/source", "/source-other"))
	assert.False(t, containsDir("/source", "/"))
	assert.False(t, containsDir("/source", "relative"))
}

func TestBaseAndStrategyPrecedence(t *testing.T) {
	subject := Subject{Kind: componentSubject, Distro: projectconfig.DistroVersionDefinition{
		TestBaseImages: map[string]string{"vm": "distro"},
	}}
	test := projectconfig.ResolvedTest{Definition: projectconfig.TestDefinition{Type: "tmt"}}
	requirements := testframework.Requirements{ImageRequired: true, ImageFormats: []string{"qcow2"}}
	options := Options{BaseImageSource: "local"}
	base, err := selectBaseName(subject, test, requirements, options, "vm")
	require.NoError(t, err)
	assert.Equal(t, "distro", base)

	test.Definition.Execution.BaseImages = map[string]string{"vm": "test"}
	base, err = selectBaseName(subject, test, requirements, options, "vm")
	require.NoError(t, err)
	assert.Equal(t, "test", base)

	options.BaseImage = "cli"
	base, err = selectBaseName(subject, test, requirements, options, "vm")
	require.NoError(t, err)
	assert.Equal(t, "cli", base)

	options.BaseImagePath = "/explicit.qcow2"
	base, err = selectBaseName(subject, test, requirements, options, "vm")
	require.NoError(t, err)
	assert.Equal(t, "cli", base, "an explicit path must not replace a selected image's name")
	assert.Equal(t, "vm", executionStrategy(test, ""))
	test.Definition.Execution.Strategy = "container"
	assert.Equal(t, "container", executionStrategy(test, ""))
	assert.Equal(t, "ssh", executionStrategy(test, "ssh"))

	subject.Kind = imageSubject
	base, err = selectBaseName(subject, test, requirements, options, "vm")
	require.NoError(t, err)
	assert.Empty(t, base)
}

func TestImageInputIntersectionAndErrors(t *testing.T) {
	env := testutils.NewTestEnv(t)
	require.NoError(t, fileutils.WriteFile(env.TestFS, "/subject.raw", []byte("image"), fileperms.PrivateFile))

	plans := []plannedTest{
		{requirements: testframework.Requirements{ImageFormats: []string{"qcow2", "raw"}}},
		{requirements: testframework.Requirements{ImageFormats: []string{"raw"}}},
	}

	resolver := &artifacts.Resolver{Env: env.Env}
	options := Options{Arch: "x86_64", ImagePath: "/subject.raw"}
	subject := Subject{Name: "base", Kind: imageSubject}
	inputs, err := resolveInputs(subject, plans, options, resolver)
	require.NoError(t, err)
	require.Len(t, inputs, 2)
	assert.Same(t, inputs[0].Image, inputs[1].Image)
	assert.Equal(t, "/subject.raw", inputs[0].Image.Path)

	plans[1].requirements.ImageFormats = []string{"oci"}
	_, err = resolveInputs(subject, plans, options, resolver)
	require.ErrorContains(t, err, "incompatible")

	plans[1].requirements.ImageFormats = []string{"raw"}
	options.ImagePath = "/missing.raw"
	_, err = resolveInputs(subject, plans, options, resolver)
	require.ErrorContains(t, err, "matched no files")
}

func TestDryRunDoesNotPersistOrAcquireArtifacts(t *testing.T) {
	base := testutils.NewTestEnv(t)
	envOptions := azldev.NewEnvOptions()
	envOptions.Config = base.Config
	envOptions.ProjectDir = base.Env.ProjectDir()
	envOptions.Interfaces = base.TestInterfaces
	envOptions.EventListener = base.EventListener
	envOptions.DryRunnable = azldev.NewAppDryRunnable(true)
	env := azldev.NewEnv(t.Context(), envOptions)
	subject := Subject{Name: "base", Kind: imageSubject, Distro: projectconfig.DistroVersionDefinition{
		Images: map[string]projectconfig.PublishedImage{
			"base": {Architectures: map[string][]projectconfig.PublishedImageArtifact{
				"x86_64": {{Format: "qcow2", URI: "https://example.test/base"}},
			}},
		},
	}}
	tests := []projectconfig.ResolvedTest{{Name: "test", Definition: projectconfig.TestDefinition{
		Type: "pytest", Pytest: map[string]any{"working-dir": "/tests", "test-paths": []string{"test.py"}},
	}}}
	before, err := fileutils.Glob(base.TestFS, "/**/*")
	require.NoError(t, err)
	results, err := Run(env, subject, tests,
		Options{Arch: "x86_64", ArtifactSource: "lkg", JUnitXMLPath: "/report.xml"}, &artifacts.Resolver{Env: env})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, testframework.Planned, results[0].Status)
	assert.Empty(t, base.CommandsExecuted)
	after, err := fileutils.Glob(base.TestFS, "/**/*")
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestUnrelatedSourceOverrideIsRejected(t *testing.T) {
	tests := []projectconfig.ResolvedTest{{Definition: projectconfig.TestDefinition{Type: "lisa"}}}
	require.ErrorContains(t, validateOverrides(tests, Options{SourceDir: "/source"}), "'--source-dir'")
}

func TestComponentInputsReuseSupportingImageOnlyForConsumers(t *testing.T) {
	env := testutils.NewTestEnv(t)
	require.NoError(t, fileutils.WriteFile(env.TestFS, "/pkg.rpm", []byte("package"), fileperms.PrivateFile))

	builds := 0
	buildErr := errors.New("support build failed")
	resolver := &artifacts.Resolver{Env: env.Env, BuildImage: func(name, arch string) ([]string, error) {
		builds++

		assert.Equal(t, "base", name)
		assert.Equal(t, "aarch64", arch)
		require.NoError(t, fileutils.WriteFile(env.TestFS, "/base.qcow2", []byte("image"), fileperms.PrivateFile))

		return []string{"/base.qcow2"}, nil
	}}
	consumer := plannedTest{baseName: "base", requirements: testframework.Requirements{
		ImageRequired: true, ImageFormats: []string{"qcow2"},
	}}
	plans := []plannedTest{consumer, {}, consumer}
	options := Options{Arch: "aarch64", RPMPaths: []string{"/pkg.rpm"}, RebuildBaseImage: true}
	subject := Subject{Name: "pkg", Kind: componentSubject}
	inputs, err := resolveInputs(subject, plans, options, resolver)
	require.NoError(t, err)
	assert.Equal(t, 1, builds)
	assert.Equal(t, inputs[0].Image, inputs[2].Image)
	assert.Nil(t, inputs[1].Image)

	for _, input := range inputs {
		require.Len(t, input.Packages, 1)
		assert.Equal(t, "/pkg.rpm", input.Packages[0].Path)
	}

	resolver.BuildImage = func(string, string) ([]string, error) { return nil, buildErr }
	_, err = resolveInputs(subject, plans, options, resolver)
	require.ErrorIs(t, err, buildErr)
}

func TestRunPersistsFailureAndFailFastOutcomes(t *testing.T) {
	for _, failFast := range []bool{false, true} {
		env := testutils.NewTestEnv(t)
		require.NoError(t, fileutils.WriteFile(env.TestFS, "/pkg.rpm", []byte("package"), fileperms.PrivateFile))

		tests := []projectconfig.ResolvedTest{
			{Name: "first", Definition: projectconfig.TestDefinition{
				Type: "pytest", Pytest: map[string]any{"working-dir": "/missing", "test-paths": []string{"test.py"}},
			}},
		}
		second := tests[0]
		second.Name = "second"
		tests = append(tests, second)
		results, err := Run(env.Env, Subject{Name: "pkg", Kind: componentSubject}, tests,
			Options{RPMPaths: []string{"/pkg.rpm"}, FailFast: failFast, JUnitXMLPath: "/combined.xml"},
			&artifacts.Resolver{Env: env.Env})
		require.Error(t, err)
		require.Len(t, results, 2)
		assert.Equal(t, testframework.Errored, results[0].Status)

		want := testframework.Errored
		if failFast {
			want = testframework.Skipped
		}

		assert.Equal(t, want, results[1].Status)
		data, readErr := fileutils.ReadFile(env.TestFS, filepath.Join(filepath.Dir(results[0].OutputDir), "results.json"))
		require.NoError(t, readErr)
		assert.Contains(t, string(data), `"name": "second"`)

		report, readErr := fileutils.ReadFile(env.TestFS, "/combined.xml")
		require.NoError(t, readErr)
		assert.Contains(t, string(report), "<error")

		if failFast {
			assert.Contains(t, string(report), "<skipped")
		}
	}
}
