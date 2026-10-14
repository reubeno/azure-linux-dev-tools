// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lisa

import (
	"fmt"
	"path/filepath"

	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"gopkg.in/yaml.v3"
)

const bootTimeoutSeconds = 300

const candidateScriptName = "install-candidates.sh"

// Batch package-manager operations preserve dependencies and report conflicts.
// LISA's rpm_package_installer uses --force --nodeps, which can hide invalid candidate sets.
const candidateInstallScript = `#!/bin/bash
set -euo pipefail
cd /home/lisa/.azldev-candidates
if command -v dnf >/dev/null 2>&1; then
    dnf -y install ./*.rpm
    dnf -y reinstall ./*.rpm
elif command -v tdnf >/dev/null 2>&1; then
    tdnf -y install ./*.rpm
    tdnf -y reinstall ./*.rpm
else
    echo "Candidate installation requires dnf or tdnf" >&2
    exit 1
fi
`

func (r *Runner) runbook(frameworkDir, keyPath string, request testframework.RunRequest) ([]byte, error) {
	testcases := make([]map[string]any, 0, len(r.criteria))
	for _, selection := range r.criteria {
		testcases = append(testcases, map[string]any{"criteria": selection})
	}

	notifiers := []map[string]any{{"type": "html"}}
	if request.JUnitPath != "" {
		notifiers = append(notifiers, map[string]any{"type": "junit", "path": request.JUnitPath})
	}

	runbook := map[string]any{
		"name": r.name,
		"include": []map[string]string{{
			"path": filepath.Join(frameworkDir, "lisa", "microsoft", "runbook", "tiers", "tier.yml"),
		}},
		"extension":              []string{filepath.Join(frameworkDir, "lisa", "microsoft", "testsuites")},
		"testcase":               testcases,
		"notifier":               notifiers,
		"exit_with_failed_count": true,
		"platform": []map[string]any{{
			"type": "qemu", "admin_username": "lisa", "admin_private_key_file": keyPath,
			"keep_environment": "no",
			"qemu":             map[string]any{"network_boot_timeout": bootTimeoutSeconds},
			"requirement":      map[string]any{"qemu": map[string]string{"qcow2": request.ImagePath}},
		}},
	}
	if len(request.RPMPaths) > 0 {
		transformers, err := candidateTransformers(request.RPMPaths, filepath.Dir(frameworkDir))
		if err != nil {
			return nil, err
		}

		runbook["transformer"] = transformers
	}

	data, err := yaml.Marshal(runbook)
	if err != nil {
		return nil, fmt.Errorf("marshal LISA runbook:\n%w", err)
	}

	return data, nil
}

// Upload all candidates before installation. Reinstall also replaces same-version local rebuilds.
func candidateTransformers(paths []string, scriptDir string) ([]map[string]any, error) {
	const destination = "/home/lisa/.azldev-candidates"

	const installerSteps = 2

	result := make([]map[string]any, 0, len(paths)+installerSteps)
	dependencies := make([]string, 0, len(paths)+1)
	names := make(map[string]bool)

	for index, path := range paths {
		upload := fmt.Sprintf("azldev_upload_%d", index)

		name := filepath.Base(path)
		if names[name] {
			return nil, fmt.Errorf("duplicate candidate RPM filename %#q", name)
		}

		names[name] = true
		result = append(result, map[string]any{
			"type": "file_uploader", "name": upload, "phase": "environment_connected",
			"source": filepath.Dir(path), "destination": destination,
			"files": []string{name}, "exit_on_error": true,
		})
		dependencies = append(dependencies, upload)
	}

	result = append(result, map[string]any{
		"type": "file_uploader", "name": "azldev_upload_installer", "phase": "environment_connected",
		"source": scriptDir, "destination": destination, "files": []string{candidateScriptName}, "exit_on_error": true,
	})
	dependencies = append(dependencies, "azldev_upload_installer")
	result = append(result, map[string]any{
		"type": "script_file", "name": "azldev_install_candidates", "phase": "environment_connected",
		"depends_on": dependencies, "exit_on_error": true, "reboot": true,
		"scripts": []map[string]any{{
			"script": destination + "/" + candidateScriptName, "interpreter": "bash", "args": "", "expected_exit_code": 0,
		}},
	})

	return result, nil
}
