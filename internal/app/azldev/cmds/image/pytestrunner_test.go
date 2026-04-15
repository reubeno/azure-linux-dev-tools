// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/cmds/image"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	pytestfixtures "github.com/microsoft/azure-linux-dev-tools/python"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----- BuildPytestArgs tests -----

func TestBuildPytestArgs_MinimalArgs(t *testing.T) {
	args := image.BuildPytestArgs(
		"/azldev-tests",
		"/azldev-image/image.qcow2",
		"", // no config
		"", // no manifest
		"", // no junit-xml
		"/azldev-fixtures",
	)

	assert.Contains(t, args, "env")
	assert.Contains(t, args, "PYTHONPATH=/azldev-fixtures")
	assert.Contains(t, args, "python3")
	assert.Contains(t, args, "-m")
	assert.Contains(t, args, "pytest")
	assert.Contains(t, args, "/azldev-tests")
	assert.Contains(t, args, "-v")
	assert.Contains(t, args, "--azldev-image")
	assert.Contains(t, args, "/azldev-image/image.qcow2")

	// Should NOT contain optional flags when not provided.
	assert.NotContains(t, args, "--azldev-config")
	assert.NotContains(t, args, "--azldev-manifest")
	assert.NotContains(t, args, "--junit-xml")
}

func TestBuildPytestArgs_WithAllOptions(t *testing.T) {
	args := image.BuildPytestArgs(
		"/azldev-tests",
		"/azldev-image/image.qcow2",
		"/azldev-config/azldev-image-config.json",
		"/azldev-manifest/image.packages",
		"/output/results.xml",
		"/azldev-fixtures",
	)

	assert.Contains(t, args, "--azldev-config")
	assert.Contains(t, args, "--azldev-manifest")
	assert.Contains(t, args, "/azldev-manifest/image.packages")
	assert.Contains(t, args, "--junit-xml")
	assert.Contains(t, args, "/output/results.xml")
}

func TestBuildPytestArgs_ConfigPathIsRemappedToMockChroot(t *testing.T) {
	args := image.BuildPytestArgs(
		"/azldev-tests",
		"/azldev-image/image.qcow2",
		"/some/host/path/azldev-image-config.json",
		"", "", "/azldev-fixtures",
	)

	// The config path argument should be remapped to the mock chroot path.
	configIdx := -1

	for i, arg := range args {
		if arg == "--azldev-config" {
			configIdx = i

			break
		}
	}

	require.Greater(t, configIdx, -1, "--azldev-config should be present")
	require.Less(t, configIdx+1, len(args), "value should follow --azldev-config")
	assert.Equal(t, "/azldev-config/azldev-image-config.json", args[configIdx+1])
}

func TestBuildPytestArgs_PythonPathIsSetCorrectly(t *testing.T) {
	args := image.BuildPytestArgs(
		"/azldev-tests",
		"/azldev-image/image.qcow2",
		"", "", "",
		"/custom/fixtures/path",
	)

	assert.Equal(t, "env", args[0])
	assert.Equal(t, "PYTHONPATH=/custom/fixtures/path", args[1])
}

func TestBuildPytestArgs_PluginRegistered(t *testing.T) {
	args := image.BuildPytestArgs(
		"/azldev-tests",
		"/azldev-image/image.qcow2",
		"", "", "",
		"/azldev-fixtures",
	)

	// Should register the conftest as a plugin via -p.
	pluginIdx := -1

	for i, arg := range args {
		if arg == "-p" {
			pluginIdx = i

			break
		}
	}

	require.Greater(t, pluginIdx, -1, "-p should be present")
	require.Less(t, pluginIdx+1, len(args), "plugin name should follow -p")
	assert.Equal(t, "azldev_check.conftest", args[pluginIdx+1])
}

// ----- SerializeImageConfigToJSON tests -----

func TestSerializeImageConfigToJSON(t *testing.T) {
	ctx := testctx.NewCtx()
	dir := "/tmp/test-config"
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), dir))

	imageConfig := &projectconfig.ImageConfig{
		Name:        "test-image",
		Description: "A test image",
		Definition: projectconfig.ImageDefinition{
			DefinitionType: projectconfig.ImageDefinitionTypeKiwi,
			Path:           "/images/test",
			Profile:        "default",
		},
		Tests: []string{"smoke", "regression"},
	}

	outputPath, err := image.SerializeImageConfigToJSON(ctx.FS(), imageConfig, dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "azldev-image-config.json"), outputPath)

	// Read back and verify.
	data, err := fileutils.ReadFile(ctx.FS(), outputPath)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, "test-image", parsed["imageName"])
	assert.Equal(t, "A test image", parsed["imageDescription"])

	definition, definitionOk := parsed["definition"].(map[string]interface{})
	require.True(t, definitionOk)
	assert.Equal(t, "kiwi", definition["type"])
	assert.Equal(t, "/images/test", definition["path"])
	assert.Equal(t, "default", definition["profile"])

	tests, testsOk := parsed["tests"].([]interface{})
	require.True(t, testsOk)
	assert.Len(t, tests, 2)
	assert.Equal(t, "smoke", tests[0])
	assert.Equal(t, "regression", tests[1])
}

func TestSerializeImageConfigToJSON_MinimalConfig(t *testing.T) {
	ctx := testctx.NewCtx()
	dir := "/tmp/test-config-minimal"
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), dir))

	imageConfig := &projectconfig.ImageConfig{
		Name: "minimal",
	}

	outputPath, err := image.SerializeImageConfigToJSON(ctx.FS(), imageConfig, dir)
	require.NoError(t, err)

	data, err := fileutils.ReadFile(ctx.FS(), outputPath)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, "minimal", parsed["imageName"])
	// Optional fields should be omitted or empty.
	_, hasDescription := parsed["imageDescription"]
	assert.False(t, hasDescription, "empty description should be omitted")
}

// ----- ExtractTo tests -----

func TestExtractTo(t *testing.T) {
	ctx := testctx.NewCtx()
	destDir := "/tmp/test-fixtures"
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), destDir))

	err := pytestfixtures.ExtractTo(ctx.FS(), destDir)
	require.NoError(t, err)

	// Verify the azldev_check package directory was created.
	packageDir := filepath.Join(destDir, "azldev_check")
	exists, err := fileutils.DirExists(ctx.FS(), packageDir)
	require.NoError(t, err)
	assert.True(t, exists, "azldev_check directory should exist")

	// Verify key Python files exist.
	expectedFiles := []string{
		"__init__.py",
		"conftest.py",
		"manifest.py",
		"imageaccess.py",
		"types.py",
	}

	for _, fileName := range expectedFiles {
		filePath := filepath.Join(packageDir, fileName)
		fileExists, err := fileutils.Exists(ctx.FS(), filePath)
		require.NoError(t, err, "checking %s", fileName)
		assert.True(t, fileExists, "%s should exist", fileName)
	}
}

func TestExtractTo_FilesContainValidPython(t *testing.T) {
	ctx := testctx.NewCtx()
	destDir := "/tmp/test-fixtures-content"
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), destDir))

	require.NoError(t, pytestfixtures.ExtractTo(ctx.FS(), destDir))

	// Verify __init__.py has expected content.
	initContent, err := fileutils.ReadFile(ctx.FS(), filepath.Join(destDir, "azldev_check", "__init__.py"))
	require.NoError(t, err)
	assert.Contains(t, string(initContent), "azldev_check")

	// Verify conftest.py has the pytest plugin hooks.
	conftestContent, err := fileutils.ReadFile(ctx.FS(), filepath.Join(destDir, "azldev_check", "conftest.py"))
	require.NoError(t, err)
	assert.Contains(t, string(conftestContent), "pytest_addoption")
	assert.Contains(t, string(conftestContent), "--azldev-image")
	assert.Contains(t, string(conftestContent), "--azldev-config")
	assert.Contains(t, string(conftestContent), "--azldev-manifest")

	// Verify manifest.py has the parser.
	manifestContent, err := fileutils.ReadFile(ctx.FS(), filepath.Join(destDir, "azldev_check", "manifest.py"))
	require.NoError(t, err)
	assert.Contains(t, string(manifestContent), "parse_packages_file")
	assert.Contains(t, string(manifestContent), "PackageInfo")

	// Verify types.py has dataclasses.
	typesContent, err := fileutils.ReadFile(ctx.FS(), filepath.Join(destDir, "azldev_check", "types.py"))
	require.NoError(t, err)
	assert.Contains(t, string(typesContent), "PackageInfo")
	assert.Contains(t, string(typesContent), "FileEntry")
	assert.Contains(t, string(typesContent), "ImageConfig")

	// Verify imageaccess.py has mount_image.
	accessContent, err := fileutils.ReadFile(ctx.FS(), filepath.Join(destDir, "azldev_check", "imageaccess.py"))
	require.NoError(t, err)
	assert.Contains(t, string(accessContent), "mount_image")
	assert.Contains(t, string(accessContent), "list_files")
}

func TestExtractTo_IsIdempotent(t *testing.T) {
	ctx := testctx.NewCtx()
	destDir := "/tmp/test-fixtures-idem"
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), destDir))

	// Extract twice — should not fail.
	require.NoError(t, pytestfixtures.ExtractTo(ctx.FS(), destDir))
	require.NoError(t, pytestfixtures.ExtractTo(ctx.FS(), destDir))
}
