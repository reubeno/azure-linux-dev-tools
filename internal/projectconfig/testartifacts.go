// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package projectconfig

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// TestExecutionConfig adds optional defaults without changing framework-specific subtables.
type TestExecutionConfig struct {
	Strategy   string            `toml:"strategy,omitempty"    json:"strategy,omitempty"   jsonschema:"enum=vm,enum=container,enum=ssh,enum=host,title=Strategy,description=Default local test execution strategy"`
	BaseImages map[string]string `toml:"base-images,omitempty" json:"baseImages,omitempty" jsonschema:"title=Base images,description=Named supporting image for each execution strategy (vm or container)"`
}

func (c TestExecutionConfig) Validate() error {
	if c.Strategy != "" && !slices.Contains([]string{"vm", "container", "ssh", "host"}, c.Strategy) {
		return fmt.Errorf("unknown test execution strategy %#q", c.Strategy)
	}

	return validateBaseImageNames(c.BaseImages)
}

// PublishedImage describes consumable images, not how to build them.
type PublishedImage struct {
	Architectures map[string][]PublishedImageArtifact `toml:"architectures" json:"architectures" jsonschema:"required,title=Architecture variants,description=Image artifacts by target architecture"`
}

type PublishedImageArtifact struct {
	Format string `toml:"format"           json:"format"           jsonschema:"required,enum=qcow2,enum=raw,enum=vhd,enum=vhdx,enum=oci,title=Format"`
	URI    string `toml:"uri"              json:"uri"              jsonschema:"required,title=Location,description=HTTP(S) disk/archive URL or docker:// OCI registry reference"`
	SHA256 string `toml:"sha256,omitempty" json:"sha256,omitempty" jsonschema:"title=SHA-256,description=Optional expected download digest"`
}

// PublishedPackages pins a coherent component output set rather than choosing each package independently.
type PublishedPackages struct {
	Architectures map[string][]PublishedPackageArtifact `toml:"architectures" json:"architectures" jsonschema:"required,title=Architecture variants,description=Complete binary package set by target architecture"`
}

type PublishedPackageArtifact struct {
	URI    string `toml:"uri"              json:"uri"              jsonschema:"required,title=RPM location,description=HTTP(S) URL of a binary RPM"`
	SHA256 string `toml:"sha256,omitempty" json:"sha256,omitempty" jsonschema:"title=SHA-256,description=Optional expected download digest"`
}

func validateBaseImageNames(images map[string]string) error {
	for strategy, name := range images {
		if strategy != "vm" && strategy != "container" {
			return fmt.Errorf("base images are only valid for 'vm' or 'container', not %#q", strategy)
		}

		if err := validateArtifactName(name); err != nil {
			return err
		}
	}

	return nil
}

func validateArtifactName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) ||
		strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid artifact name %#q", name)
	}

	return nil
}

func validatePublishedLocation(uri, digest string, oci bool) error {
	location, err := url.Parse(uri)
	if err != nil || location.Host == "" || location.User != nil ||
		(location.Scheme != "https" && location.Scheme != "http" && (!oci || location.Scheme != "docker")) {
		return fmt.Errorf("invalid published artifact URI %#q", uri)
	}

	if digest != "" {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("invalid SHA-256 digest %#q", digest)
		}
	}

	if location.Scheme == "docker" && digest != "" {
		return errors.New("OCI registry references use '@sha256:...' in 'uri', not the download 'sha256' field")
	}

	return nil
}

func validatePublishedArtifacts(distros map[string]DistroDefinition) error {
	for distroName, distro := range distros {
		for versionName, version := range distro.Versions {
			if err := version.validateTestArtifacts(); err != nil {
				return fmt.Errorf("distro %#q version %#q:\n%w", distroName, versionName, err)
			}
		}
	}

	return nil
}

func (v DistroVersionDefinition) validateTestArtifacts() error {
	if err := validateBaseImageNames(v.TestBaseImages); err != nil {
		return err
	}

	for _, name := range v.TestBaseImages {
		if _, exists := v.Images[name]; !exists {
			return fmt.Errorf("test base image %#q is not in the distro version's published images", name)
		}
	}

	for name, image := range v.Images {
		if err := image.validate(name); err != nil {
			return err
		}
	}

	for name, packages := range v.TestPackages {
		if err := packages.validate(name); err != nil {
			return err
		}
	}

	return nil
}

func (image PublishedImage) validate(name string) error {
	if err := validateArtifactName(name); err != nil {
		return err
	}

	if len(image.Architectures) == 0 {
		return fmt.Errorf("published image %#q has no architecture variants", name)
	}

	for arch, variants := range image.Architectures {
		if !slices.Contains(SupportedImageArchitectures(), arch) || len(variants) == 0 {
			return fmt.Errorf("published image %#q has invalid or empty architecture %#q", name, arch)
		}

		seen := make(map[string]bool)
		for _, variant := range variants {
			if !slices.Contains([]string{"qcow2", "raw", "vhd", "vhdx", "oci"}, variant.Format) || seen[variant.Format] {
				return fmt.Errorf("published image %#q has invalid or duplicate format %#q", name, variant.Format)
			}

			seen[variant.Format] = true
			if err := validatePublishedLocation(variant.URI, variant.SHA256, variant.Format == "oci"); err != nil {
				return fmt.Errorf("published image %#q:\n%w", name, err)
			}
		}
	}

	return nil
}

func (packages PublishedPackages) validate(name string) error {
	if err := validateArtifactName(name); err != nil {
		return err
	}

	if len(packages.Architectures) == 0 {
		return fmt.Errorf("published packages %#q have no architecture variants", name)
	}

	for arch, rpms := range packages.Architectures {
		if !slices.Contains(SupportedImageArchitectures(), arch) || len(rpms) == 0 {
			return fmt.Errorf("published packages %#q have invalid or empty architecture %#q", name, arch)
		}

		seen := make(map[string]bool)

		for _, rpm := range rpms {
			if err := validatePublishedLocation(rpm.URI, rpm.SHA256, false); err != nil {
				return err
			}

			if seen[rpm.URI] {
				return fmt.Errorf("duplicate published RPM URI %#q", rpm.URI)
			}

			seen[rpm.URI] = true
		}
	}

	return nil
}
