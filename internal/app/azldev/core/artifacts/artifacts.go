// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package artifacts resolves test inputs independently of the framework consuming them.
package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/downloader"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/pythonenv"
)

type Selection struct {
	Source  string
	Paths   []string
	Rebuild bool
}

func (s Selection) Validate() error {
	if s.Source != "" && s.Source != "local" && s.Source != "lkg" && s.Source != "path" {
		return fmt.Errorf("artifact source must be 'local', 'lkg', or 'path', not %#q", s.Source)
	}
	if len(s.Paths) > 0 && (s.Rebuild || s.Source == "lkg") {
		return errors.New("explicit artifact paths cannot be combined with rebuilding or LKG")
	}
	if s.Source == "path" && len(s.Paths) == 0 {
		return errors.New("artifact source 'path' requires an explicit path")
	}
	if s.Rebuild && s.Source == "lkg" {
		return errors.New("rebuilding cannot be combined with LKG")
	}

	return nil
}

// Artifact records both the consumed bytes and their provenance. BuiltAt is never inferred from download time.
type Artifact struct {
	Path       string    `json:"path"`
	Source     string    `json:"source"`
	URI        string    `json:"uri,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	Format     string    `json:"format,omitempty"`
	Arch       string    `json:"arch"`
	BuiltAt    time.Time `json:"builtAt,omitempty"`
	ModifiedAt time.Time `json:"modifiedAt,omitempty"`
}

type BuildRecord struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Arch    string    `json:"arch"`
	BuiltAt time.Time `json:"builtAt"`
	Paths   []string  `json:"paths"`
}

// Resolver delegates building to application services, avoiding command-package dependencies.
type Resolver struct {
	Env           *azldev.Env
	Downloader    downloader.Downloader
	BuildImage    func(name, arch string) ([]string, error)
	BuildPackages func(name, arch string) ([]string, error)
}

func recordPath(output, kind, name, arch string) string {
	return filepath.Join(output, "build-records", kind, name, arch+".json")
}

// RecordBuild persists exact output membership only after a successful build.
func RecordBuild(ctx opctx.Ctx, output string, record BuildRecord) error {
	if ctx.DryRun() {
		return nil
	}
	if err := testframework.ValidateName(record.Name); err != nil {
		return err
	}
	if !slices.Contains([]string{"image", "component"}, record.Kind) ||
		!slices.Contains(projectconfig.SupportedImageArchitectures(), record.Arch) {
		return errors.New("invalid build record kind or architecture")
	}
	if len(record.Paths) == 0 {
		return errors.New("cannot record a build with no outputs")
	}
	for _, path := range record.Paths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("build output path must be absolute: %#q", path)
		}
	}
	path := recordPath(output, record.Kind, record.Name, record.Arch)
	if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}

	return fileutils.WriteFile(ctx.FS(), path, data, fileperms.PrivateFile)
}

func (r *Resolver) Image(name, arch string, formats []string, selection Selection,
	distro projectconfig.DistroVersionDefinition,
) (Artifact, error) {
	if err := r.validate(name, arch, selection); err != nil {
		return Artifact{}, err
	}
	if len(selection.Paths) > 0 {
		if len(selection.Paths) != 1 {
			return Artifact{}, errors.New("an image input requires exactly one path")
		}
		paths, err := r.expandPaths(selection.Paths)
		if err != nil {
			return Artifact{}, err
		}

		return r.selectImage(paths, arch, formats, "path", time.Time{})
	}
	if selection.Source == "lkg" {
		image, ok := distro.Images[name]
		if !ok {
			return Artifact{}, fmt.Errorf("no published image %#q in the selected distro version", name)
		}
		for _, format := range formats {
			for _, variant := range image.Architectures[arch] {
				if variant.Format == format {
					return r.fetch(variant.URI, variant.SHA256, variant.Format, arch)
				}
			}
		}

		return Artifact{}, fmt.Errorf("published image %#q has no compatible %#q artifact for %s", name, formats, arch)
	}
	paths, builtAt, err := r.local("image", name, arch, selection.Rebuild)
	if err != nil {
		return Artifact{}, err
	}
	if len(paths) == 0 {
		if r.BuildImage == nil {
			return Artifact{}, fmt.Errorf("no local build recipe available for image %#q", name)
		}
		if r.Env.DryRun() {
			slog.Info("Would build missing image", "name", name, "arch", arch)

			return Artifact{Path: filepath.Join(r.Env.OutputDir(), "images", name, name+"."+formats[0]),
				Source: "local", Arch: arch, Format: formats[0]}, nil
		}
		paths, err = r.BuildImage(name, arch)
		if err != nil {
			return Artifact{}, err
		}
		builtAt = time.Now().UTC()
		if err := RecordBuild(r.Env, r.Env.OutputDir(), BuildRecord{
			Name: name, Kind: "image", Arch: arch, BuiltAt: builtAt, Paths: paths,
		}); err != nil {
			return Artifact{}, err
		}
	}

	return r.selectImage(paths, arch, formats, "local", builtAt)
}

func (r *Resolver) Packages(name, arch string, selection Selection,
	distro projectconfig.DistroVersionDefinition,
) ([]Artifact, error) {
	if err := r.validate(name, arch, selection); err != nil {
		return nil, err
	}
	if selection.Source == "lkg" {
		packages := distro.TestPackages[name].Architectures[arch]
		if len(packages) == 0 {
			return nil, fmt.Errorf("no published package set for component %#q and architecture %#q", name, arch)
		}
		result := make([]Artifact, 0, len(packages))
		for _, rpm := range packages {
			artifact, err := r.fetch(rpm.URI, rpm.SHA256, "rpm", arch)
			if err != nil {
				return nil, err
			}
			result = append(result, artifact)
		}

		return result, nil
	}
	var paths []string
	var builtAt time.Time
	var err error
	source := "local"
	if len(selection.Paths) > 0 {
		source = "path"
		paths, err = r.expandPaths(selection.Paths)
	} else {
		paths, builtAt, err = r.local("component", name, arch, selection.Rebuild)
	}
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		if r.BuildPackages == nil {
			return nil, fmt.Errorf("no builder available for component %#q", name)
		}
		if r.Env.DryRun() {
			slog.Info("Would build missing component", "name", name, "arch", arch)

			return []Artifact{{Path: filepath.Join(r.Env.OutputDir(), "rpms", name+".rpm"),
				Source: "local", Arch: arch, Format: "rpm"}}, nil
		}
		paths, err = r.BuildPackages(name, arch)
		if err != nil {
			return nil, err
		}
		builtAt = time.Now().UTC()
		if err := RecordBuild(r.Env, r.Env.OutputDir(), BuildRecord{
			Name: name, Kind: "component", Arch: arch, BuiltAt: builtAt, Paths: paths,
		}); err != nil {
			return nil, err
		}
	}
	result := make([]Artifact, 0, len(paths))
	for _, path := range paths {
		if !strings.HasSuffix(path, ".rpm") || strings.HasSuffix(path, ".src.rpm") {
			return nil, fmt.Errorf("candidate package must be a binary RPM: %#q", path)
		}
		artifact, err := r.describe(path, "rpm", arch, source, builtAt)
		if err != nil {
			return nil, err
		}
		result = append(result, artifact)
	}

	return result, nil
}

func (r *Resolver) validate(name, arch string, selection Selection) error {
	if err := testframework.ValidateName(name); err != nil {
		return err
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
	data, err := fileutils.ReadFile(r.Env.FS(), recordPath(r.Env.OutputDir(), kind, name, arch))
	if err == nil {
		var record BuildRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, time.Time{}, fmt.Errorf("invalid build record for %#q:\n%w", name, err)
		}
		if record.Name != name || record.Kind != kind || record.Arch != arch || len(record.Paths) == 0 {
			return nil, time.Time{}, fmt.Errorf("build record does not match %s %#q (%s)", kind, name, arch)
		}
		for _, path := range record.Paths {
			if !filepath.IsAbs(path) {
				return nil, time.Time{}, fmt.Errorf("non-absolute path in build record: %#q", path)
			}
			if _, err := r.Env.FS().Stat(path); errors.Is(err, fs.ErrNotExist) {
				slog.Warn("Recorded build output is missing; rebuilding", "path", path)

				return nil, time.Time{}, nil
			} else if err != nil {
				return nil, time.Time{}, err
			}
		}

		return record.Paths, record.BuiltAt, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, time.Time{}, err
	}
	if kind == "image" {
		paths, err := fileutils.Glob(r.Env.FS(), filepath.Join(r.Env.OutputDir(), "images", name, "*"))
		if err != nil {
			return nil, time.Time{}, err
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
	if err != nil || len(paths) == 0 {
		return nil, time.Time{}, err
	}
	if r.Env.DryRun() {
		slog.Info("Legacy RPM output requires header inspection; would resolve component build", "component", name)

		return nil, time.Time{}, nil
	}
	if err := prereqs.RequireExecutable(r.Env, "rpm", nil); err != nil {
		return nil, time.Time{}, err
	}
	var selected []string
	sourceBuild := ""
	for _, path := range paths {
		if strings.HasSuffix(path, ".src.rpm") {
			continue
		}
		raw := exec.CommandContext(r.Env, "rpm", "-qp", "--qf", "%{SOURCERPM}\\t%{ARCH}", path)
		cmd, err := r.Env.Command(raw)
		if err != nil {
			return nil, time.Time{}, err
		}
		output, err := cmd.RunAndGetOutput(r.Env)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("read RPM identity %#q:\n%w", path, err)
		}
		fields := strings.Split(output, "\t")
		if len(fields) != 2 {
			return nil, time.Time{}, fmt.Errorf("unexpected RPM identity for %#q", path)
		}
		parts := strings.Split(strings.TrimSuffix(fields[0], ".src.rpm"), "-")
		if len(parts) < 3 || strings.Join(parts[:len(parts)-2], "-") != name ||
			(fields[1] != arch && fields[1] != "noarch") {
			continue
		}
		if sourceBuild != "" && sourceBuild != fields[0] {
			return nil, time.Time{}, fmt.Errorf("multiple local builds of %#q; use '--rebuild' or explicit '--rpm' paths", name)
		}
		sourceBuild = fields[0]
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
			return nil, err
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
		return Artifact{}, err
	}
	if !info.Mode().IsRegular() {
		return Artifact{}, fmt.Errorf("artifact %#q must be a regular file", path)
	}
	artifact := Artifact{Path: path, Format: format, Arch: arch, Source: source,
		BuiltAt: builtAt, ModifiedAt: info.ModTime()}
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
		name string
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

func (r *Resolver) fetch(uri, digest, format, arch string) (Artifact, error) {
	if strings.HasPrefix(uri, "docker://") {
		return r.pullContainer(strings.TrimPrefix(uri, "docker://"), arch)
	}
	location, err := url.Parse(uri)
	if err != nil || (location.Scheme != "https" && location.Scheme != "http") || location.Host == "" {
		return Artifact{}, fmt.Errorf("invalid artifact download URL %#q", uri)
	}
	filename := filepath.Base(location.Path)
	if filename == "." || filename == "/" {
		return Artifact{}, fmt.Errorf("artifact URL has no filename: %#q", uri)
	}
	cache := filepath.Join(r.Env.WorkDir(), "artifacts", pythonenv.Key(uri, digest, arch), filename)
	if r.Env.DryRun() {
		slog.Info("Would fetch LKG artifact", "uri", uri, "path", cache)

		return Artifact{Path: cache, Source: "lkg", URI: uri, Format: format, Arch: arch}, nil
	}
	download := r.Downloader
	if download == nil {
		download, err = downloader.NewHTTPDownloader(r.Env, r.Env, r.Env.FS())
		if err != nil {
			return Artifact{}, err
		}
	}
	exists, err := fileutils.Exists(r.Env.FS(), cache)
	if err != nil {
		return Artifact{}, err
	}
	// Unpinned LKG URLs can change. Refresh them once per resolution rather than treating a cache hit as current.
	if digest == "" || !exists {
		if err := download.Download(r.Env, uri, cache); err != nil {
			return Artifact{}, err
		}
	}
	actual, err := hashFile(r.Env.FS(), cache)
	if err != nil {
		return Artifact{}, err
	}
	if digest != "" && !strings.EqualFold(digest, actual) {
		return Artifact{}, fmt.Errorf("SHA-256 mismatch for %#q: expected %s, got %s", uri, digest, actual)
	}
	artifact, err := r.describe(cache, format, arch, "lkg", time.Time{})
	artifact.URI, artifact.SHA256 = uri, actual
	slog.Info("Resolved LKG artifact", "uri", uri, "sha256", actual, "build-age", "unknown")

	return artifact, err
}

func hashFile(files opctx.FS, path string) (string, error) {
	file, err := files.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (r *Resolver) pullContainer(reference, arch string) (Artifact, error) {
	result := Artifact{Path: reference, URI: "docker://" + reference, Source: "lkg", Format: "oci", Arch: arch}
	if r.Env.DryRun() {
		slog.Info("Would pull container image", "reference", reference, "arch", arch)

		return result, nil
	}
	if err := prereqs.RequireExecutable(r.Env, "podman", nil); err != nil {
		return Artifact{}, err
	}
	ociArch := "amd64"
	if arch == "aarch64" {
		ociArch = "arm64"
	}
	if err := testframework.Command(r.Env, "", "podman", "pull", "--arch", ociArch, reference); err != nil {
		return Artifact{}, err
	}
	cmd, err := r.Env.Command(exec.CommandContext(r.Env, "podman", "image", "inspect", "--format", "{{.Id}}", reference))
	if err != nil {
		return Artifact{}, err
	}
	identity, err := cmd.RunAndGetOutput(r.Env)
	if err != nil {
		return Artifact{}, err
	}
	if identity == "" {
		return Artifact{}, errors.New("podman did not return a container image identity")
	}
	result.Path = identity
	slog.Info("Resolved LKG container", "reference", reference, "identity", identity, "build-age", "unknown")

	return result, nil
}
