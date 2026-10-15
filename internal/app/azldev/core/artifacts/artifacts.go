// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package artifacts resolves test inputs independently of the framework consuming them.
package artifacts

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/downloader"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
)

type Selection struct {
	Source  string
	Paths   []string
	Rebuild bool
}

const (
	SourceLKG  = "lkg"
	SourcePath = "path"
)

func (s Selection) Validate() error {
	if s.Source != "" && s.Source != "local" && s.Source != SourceLKG && s.Source != SourcePath {
		return fmt.Errorf("artifact source must be 'local', 'lkg', or 'path', not %#q", s.Source)
	}

	if len(s.Paths) > 0 && (s.Rebuild || s.Source == SourceLKG) {
		return errors.New("explicit artifact paths cannot be combined with rebuilding or LKG")
	}

	if s.Source == SourcePath && len(s.Paths) == 0 {
		return errors.New("artifact source 'path' requires an explicit path")
	}

	if s.Rebuild && s.Source == SourceLKG {
		return errors.New("rebuilding cannot be combined with LKG")
	}

	return nil
}

type Artifact = artifact.Artifact

// Resolver delegates building to services that publish their normal build manifests.
type Resolver struct {
	Env           *azldev.Env
	Downloader    downloader.Downloader
	BuildImage    func(name, arch string) ([]string, error)
	BuildPackages func(name, arch string) ([]string, error)
}

func (r *Resolver) Image(name, arch string, formats []string, selection Selection,
	distro projectconfig.DistroVersionDefinition,
) (Artifact, error) {
	if len(formats) == 0 {
		return Artifact{}, errors.New("image resolution requires at least one supported format")
	}

	if err := r.validate(name, arch, selection); err != nil {
		return Artifact{}, fmt.Errorf("image:\n%w", err)
	}

	if len(selection.Paths) > 0 {
		if len(selection.Paths) != 1 {
			return Artifact{}, errors.New("an image input requires exactly one path")
		}

		paths, err := r.expandPaths(selection.Paths)
		if err != nil {
			return Artifact{}, fmt.Errorf("image:\n%w", err)
		}

		return r.selectImage(paths, arch, formats, SourcePath, time.Time{})
	}

	if selection.Source == SourceLKG {
		return r.publishedImage(name, arch, formats, distro)
	}

	paths, builtAt, err := r.local("image", name, arch, selection.Rebuild)
	if err != nil {
		return Artifact{}, fmt.Errorf("image:\n%w", err)
	}

	if len(paths) == 0 {
		if r.BuildImage == nil {
			return Artifact{}, fmt.Errorf("no local build recipe available for image %#q", name)
		}

		if r.Env.DryRun() {
			slog.Info("Would build missing image", "name", name, "arch", arch)

			return Artifact{
				Path:   filepath.Join(r.Env.OutputDir(), "images", name, name+"."+formats[0]),
				Source: "local", Arch: arch, Format: formats[0],
			}, nil
		}

		paths, err = r.BuildImage(name, arch)
		if err != nil {
			return Artifact{}, fmt.Errorf("image:\n%w", err)
		}

		builtAt = time.Now().UTC()
	}

	return r.selectImage(paths, arch, formats, "local", builtAt)
}

func (r *Resolver) Packages(name, arch string, selection Selection,
	distro projectconfig.DistroVersionDefinition,
) ([]Artifact, error) {
	if err := r.validate(name, arch, selection); err != nil {
		return nil, fmt.Errorf("packages:\n%w", err)
	}

	if selection.Source == SourceLKG {
		return r.publishedPackages(name, arch, distro)
	}

	var (
		paths   []string
		builtAt time.Time
		err     error
	)

	source := "local"
	if len(selection.Paths) > 0 {
		source = SourcePath
		paths, err = r.expandPaths(selection.Paths)
	} else {
		paths, builtAt, err = r.local("component", name, arch, selection.Rebuild)
	}

	if err != nil {
		return nil, fmt.Errorf("packages:\n%w", err)
	}

	if len(paths) == 0 {
		if r.BuildPackages == nil {
			return nil, fmt.Errorf("no builder available for component %#q", name)
		}

		if r.Env.DryRun() {
			slog.Info("Would build missing component", "name", name, "arch", arch)

			return []Artifact{{
				Path:   filepath.Join(r.Env.OutputDir(), "rpms", name+".rpm"),
				Source: "local", Arch: arch, Format: "rpm",
			}}, nil
		}

		paths, err = r.BuildPackages(name, arch)
		if err != nil {
			return nil, fmt.Errorf("packages:\n%w", err)
		}

		builtAt = time.Now().UTC()
	}

	result := make([]Artifact, 0, len(paths))
	for _, path := range paths {
		if !strings.HasSuffix(path, ".rpm") || strings.HasSuffix(path, ".src.rpm") {
			return nil, fmt.Errorf("candidate package must be a binary RPM: %#q", path)
		}

		artifact, err := r.describe(path, "rpm", arch, source, builtAt)
		if err != nil {
			return nil, fmt.Errorf("packages:\n%w", err)
		}

		result = append(result, artifact)
	}

	return result, nil
}

func (r *Resolver) validate(name, arch string, selection Selection) error {
	if err := artifact.ValidateName(name); err != nil {
		return fmt.Errorf("validate:\n%w", err)
	}

	if !slices.Contains(projectconfig.SupportedImageArchitectures(), arch) {
		return fmt.Errorf("unsupported artifact architecture %#q", arch)
	}

	if r.Env.OutputDir() == "" || r.Env.WorkDir() == "" {
		return errors.New("artifact resolution requires project output and work directories")
	}

	return selection.Validate()
}

func (r *Resolver) local(kind, name, arch string, rebuild bool) ([]string, time.Time, error) {
	if rebuild {
		return nil, time.Time{}, nil
	}

	data, err := fileutils.ReadFile(r.Env.FS(), artifact.RecordPath(r.Env.OutputDir(), kind, name, arch))
	if err == nil {
		return r.readRecord(data, kind, name, arch)
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return nil, time.Time{}, fmt.Errorf("read build record:\n%w", err)
	}

	if kind == "image" {
		paths, err := fileutils.Glob(r.Env.FS(), filepath.Join(r.Env.OutputDir(), "images", name, "*"))
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("local:\n%w", err)
		}

		var images []string

		for _, path := range paths {
			if ImageFormat(path) != "" {
				// Legacy outputs have no architecture metadata. Require an architecture
				// token rather than accidentally testing another architecture.
				if strings.Contains(filepath.Base(path), arch) {
					images = append(images, path)
				}
			}
		}

		return images, time.Time{}, nil
	}

	return r.legacyPackages(name, arch)
}

func (r *Resolver) legacyPackages(name, arch string) ([]string, time.Time, error) {
	paths, err := fileutils.Glob(r.Env.FS(), filepath.Join(r.Env.OutputDir(), "rpms", "**", "*.rpm"))
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("find legacy RPMs:\n%w", err)
	}

	if len(paths) == 0 {
		return nil, time.Time{}, nil
	}

	if r.Env.DryRun() {
		slog.Info("Legacy RPM output requires header inspection; would resolve component build", "component", name)

		return nil, time.Time{}, nil
	}

	if err := prereqs.RequireExecutable(r.Env, "rpm", nil); err != nil {
		return nil, time.Time{}, fmt.Errorf("legacy packages:\n%w", err)
	}

	selected := make([]string, 0, len(paths))

	sourceBuild := ""

	for _, path := range paths {
		if strings.HasSuffix(path, ".src.rpm") {
			continue
		}

		sourceRPM, packageArch, err := r.rpmIdentity(path)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("read RPM identity %#q:\n%w", path, err)
		}

		parts := strings.Split(strings.TrimSuffix(sourceRPM, ".src.rpm"), "-")
		if len(parts) < 3 || strings.Join(parts[:len(parts)-2], "-") != name ||
			(packageArch != arch && packageArch != "noarch") {
			continue
		}

		if sourceBuild != "" && sourceBuild != sourceRPM {
			return nil, time.Time{}, fmt.Errorf("multiple local builds of %#q; use '--rebuild' or explicit '--rpm' paths", name)
		}

		sourceBuild = sourceRPM

		selected = append(selected, path)
	}

	return selected, time.Time{}, nil
}

// ImageFormat recognizes disk images and OCI archives without using command-package types.
func ImageFormat(path string) string {
	lower := strings.ToLower(path)
	for _, suffix := range []string{".oci.tar", ".oci.tar.gz", ".oci.tar.xz"} {
		if strings.HasSuffix(lower, suffix) {
			return "oci"
		}
	}

	format := strings.TrimPrefix(filepath.Ext(lower), ".")
	if format == "vhdfixed" {
		format = "vhd"
	}

	if slices.Contains([]string{"raw", "qcow2", "vhd", "vhdx"}, format) {
		return format
	}

	return ""
}

func (r *Resolver) selectImage(paths []string, arch string, formats []string, source string,
	builtAt time.Time,
) (Artifact, error) {
	for _, format := range formats {
		var matches []string

		for _, path := range paths {
			if ImageFormat(path) == format {
				matches = append(matches, path)
			}
		}

		if len(matches) > 1 {
			return Artifact{}, fmt.Errorf("ambiguous %s image artifacts: %v; specify an image path", format, matches)
		}

		if len(matches) == 1 {
			return r.describe(matches[0], format, arch, source, builtAt)
		}
	}

	return Artifact{}, fmt.Errorf("no image artifact compatible with formats %v; available paths: %v", formats, paths)
}

func (r *Resolver) expandPaths(patterns []string) ([]string, error) {
	var paths []string

	for _, pattern := range patterns {
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(r.Env.ProjectDir(), pattern)
		}

		matches, err := fileutils.Glob(r.Env.FS(), pattern)
		if err != nil {
			return nil, fmt.Errorf("expand paths:\n%w", err)
		}

		if len(matches) == 0 {
			return nil, fmt.Errorf("artifact path %#q matched no files", pattern)
		}

		paths = append(paths, matches...)
	}

	sort.Strings(paths)

	return slices.Compact(paths), nil
}

func (r *Resolver) describe(path, format, arch, source string, builtAt time.Time) (Artifact, error) {
	info, err := r.Env.FS().Stat(path)
	if err != nil {
		return Artifact{}, fmt.Errorf("describe:\n%w", err)
	}

	if !info.Mode().IsRegular() {
		return Artifact{}, fmt.Errorf("artifact %#q must be a regular file", path)
	}

	artifact := Artifact{
		Path: path, Format: format, Arch: arch, Source: source,
		BuiltAt: builtAt, ModifiedAt: info.ModTime(),
	}

	ageTime, basis := builtAt, "built"
	if builtAt.IsZero() {
		ageTime, basis = info.ModTime(), "file modified; build age unknown"
	}

	slog.Info("Using test artifact", "path", path, "source", source,
		"age", HumanAge(time.Since(ageTime)), "age-basis", basis)

	return artifact, nil
}

// HumanAge describes age without suggesting that age establishes freshness.
func HumanAge(age time.Duration) string {
	if age < 0 {
		return "timestamp is in the future"
	}

	for _, unit := range []struct {
		duration time.Duration
		name     string
	}{{24 * time.Hour, "day"}, {time.Hour, "hour"}, {time.Minute, "minute"}, {time.Second, "second"}} {
		if age >= unit.duration {
			count := int64(age / unit.duration)

			suffix := ""
			if count != 1 {
				suffix = "s"
			}

			return fmt.Sprintf("%d %s%s ago", count, unit.name, suffix)
		}
	}

	return "less than a second ago"
}

func (r *Resolver) pullContainer(reference, arch string) (Artifact, error) {
	result := Artifact{URI: "docker://" + reference, Source: "lkg", Format: "oci", Arch: arch}
	if r.Env.DryRun() {
		slog.Info("Would pull container image", "reference", reference, "arch", arch)

		return result, nil
	}

	if err := prereqs.RequireExecutable(r.Env, "podman", nil); err != nil {
		return Artifact{}, fmt.Errorf("pull container:\n%w", err)
	}

	ociArch := "amd64"
	if arch == "aarch64" {
		ociArch = "arm64"
	}

	// Capture this pull's immutable ID. Inspecting the tag afterwards could
	// select a different image if another run updates the same tag.
	raw := exec.CommandContext(r.Env, "podman", "pull", "--quiet", "--arch", ociArch, reference)

	var stderr strings.Builder

	raw.Stderr = &stderr

	cmd, err := r.Env.Command(raw)
	if err != nil {
		return Artifact{}, fmt.Errorf("pull container:\n%w", err)
	}

	identity, err := cmd.RunAndGetOutput(r.Env)
	if err != nil {
		return Artifact{}, fmt.Errorf("pull container (%s):\n%w", strings.TrimSpace(stderr.String()), err)
	}

	identity, err = artifact.ParseContainerImageID(identity)
	if err != nil {
		return Artifact{}, fmt.Errorf("read pulled container identity:\n%w", err)
	}

	result.OCIImageID = identity
	slog.Info("Resolved LKG container", "reference", reference, "identity", identity, "build-age", "unknown")

	return result, nil
}
