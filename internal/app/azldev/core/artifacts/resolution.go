// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package artifacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
)

func (r *Resolver) publishedImage(name, arch string, formats []string,
	distro projectconfig.DistroVersionDefinition,
) (Artifact, error) {
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

func (r *Resolver) publishedPackages(name, arch string,
	distro projectconfig.DistroVersionDefinition,
) ([]Artifact, error) {
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

func (r *Resolver) readRecord(data []byte, kind, name, arch string) ([]string, time.Time, error) {
	var record artifact.BuildRecord
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

		_, err := r.Env.FS().Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			slog.Warn("Recorded build output is missing; rebuilding", "path", path)

			return nil, time.Time{}, nil
		}

		if err != nil {
			return nil, time.Time{}, fmt.Errorf("check recorded artifact %#q:\n%w", path, err)
		}
	}

	return record.Paths, record.BuiltAt, nil
}

func (r *Resolver) rpmIdentity(path string) (sourceRPM, arch string, err error) {
	raw := exec.CommandContext(r.Env, "rpm", "-qp", "--qf", "%{SOURCERPM}\\t%{ARCH}", path)

	var stderr strings.Builder

	raw.Stderr = &stderr

	cmd, err := r.Env.Command(raw)
	if err != nil {
		return "", "", fmt.Errorf("create RPM query:\n%w", err)
	}

	output, err := cmd.RunAndGetOutput(r.Env)
	if err != nil {
		return "", "", fmt.Errorf("query RPM identity (%s):\n%w", stderr.String(), err)
	}

	sourceRPM, arch, found := strings.Cut(output, "\t")
	if !found || sourceRPM == "" || arch == "" {
		return "", "", fmt.Errorf("unexpected RPM identity for %#q", path)
	}

	return sourceRPM, arch, nil
}
