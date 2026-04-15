// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/cmds/image"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildNativePytestArgs_PlaceholderSubstitution(t *testing.T) {
	options := &image.ImageTestOptions{
		ImagePath: "/images/test.raw",
	}

	args := image.BuildNativePytestArgs(
		[]string{"cases/", "--image-path", "{image}", "-v"},
		options,
	)

	assert.Equal(t, []string{"cases/", "--image-path", "/images/test.raw", "-v"}, args)
}

func TestBuildNativePytestArgs_NoPlaceholder(t *testing.T) {
	options := &image.ImageTestOptions{
		ImagePath: "/images/test.raw",
	}

	args := image.BuildNativePytestArgs(
		[]string{"cases/", "-v"},
		options,
	)

	assert.Equal(t, []string{"cases/", "-v"}, args)
}

func TestBuildNativePytestArgs_JUnitXMLAppended(t *testing.T) {
	options := &image.ImageTestOptions{
		ImagePath:    "/images/test.raw",
		JUnitXMLPath: "/output/results.xml",
	}

	args := image.BuildNativePytestArgs(
		[]string{"cases/", "--image-path", "{image}"},
		options,
	)

	assert.Contains(t, args, "--junit-xml")
	assert.Contains(t, args, "/output/results.xml")
}

func TestBuildNativePytestArgs_JUnitXMLNotDuplicated(t *testing.T) {
	options := &image.ImageTestOptions{
		ImagePath:    "/images/test.raw",
		JUnitXMLPath: "/output/results.xml",
	}

	args := image.BuildNativePytestArgs(
		[]string{"cases/", "--junit-xml", "/other/path.xml"},
		options,
	)

	// Should not append a duplicate --junit-xml.
	junitCount := 0

	for _, arg := range args {
		if arg == "--junit-xml" {
			junitCount++
		}
	}

	assert.Equal(t, 1, junitCount, "should not duplicate --junit-xml")
}

func TestBuildNativePytestArgs_EmptyArgs(t *testing.T) {
	options := &image.ImageTestOptions{
		ImagePath: "/images/test.raw",
	}

	args := image.BuildNativePytestArgs(nil, options)
	assert.Empty(t, args)
}

func TestRunPytestSuite_MissingPytestConfig(t *testing.T) {
	// Verify that the runner requires a pytest subtable.
	testConfig := &projectconfig.TestConfig{
		Name: "smoke",
		Type: projectconfig.TestTypePytest,
		// Pytest is nil.
	}

	options := &image.ImageTestOptions{
		ImagePath: "/images/test.raw",
	}

	err := image.RunPytestSuite(nil, testConfig, options)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing pytest configuration")
}
