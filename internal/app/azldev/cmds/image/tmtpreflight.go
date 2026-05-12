// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/tmt"
)

// tmtPrereqExec describes an executable prerequisite, with the package(s) that
// provide it.
type tmtPrereqExec struct {
	name   string                 // executable name as found on PATH
	label  string                 // human label for messages
	prereq *prereqs.PackagePrereq // nil = don't auto-install
}

// tmtPrereqFile describes a file prerequisite (typically a header or pkg-config
// file installed by a `*-devel` package).
type tmtPrereqFile struct {
	path   string
	label  string
	prereq *prereqs.PackagePrereq
}

// tmtPreflight verifies the host has the executables, headers, and runtime access
// tmt will need to install and run. Header-providing packages (e.g., libvirt-devel,
// python3-devel) are required because tmt's pip extras (notably provision-virtual via
// libvirt-python) build native modules from source — there are no upstream wheels.
//
// Auto-install via [prereqs.RequireExecutable]/[prereqs.RequireFile] is offered when
// the [opctx.Ctx] policy permits, on Fedora and Azure Linux hosts.
func tmtPreflight(env *azldev.Env, tmtConfig *projectconfig.TmtConfig) error {
	for _, exec := range tmtBaseExecutables() {
		if err := prereqs.RequireExecutable(env, exec.name, exec.prereq); err != nil {
			return fmt.Errorf("%s is required to run tmt tests:\n%w", exec.label, err)
		}
	}

	for _, hdr := range tmtBuildHeaders(env) {
		if err := prereqs.RequireFile(env, hdr.label, hdr.path, hdr.prereq); err != nil {
			return fmt.Errorf("%s is required to build tmt's native dependencies:\n%w", hdr.label, err)
		}
	}

	// Provision-specific preflight.
	if tmtConfig.Provision.How == projectconfig.TmtProvisionHowVirtual {
		for _, exec := range tmtVirtualExecutables() {
			if err := prereqs.RequireExecutable(env, exec.name, exec.prereq); err != nil {
				return fmt.Errorf("%s is required for tmt provision -h virtual:\n%w", exec.label, err)
			}
		}

		for _, hdr := range tmtVirtualHeaders() {
			if err := prereqs.RequireFile(env, hdr.label, hdr.path, hdr.prereq); err != nil {
				return fmt.Errorf("%s is required for tmt provision -h virtual:\n%w", hdr.label, err)
			}
		}

		// Don't fail loudly here — libvirt access is sometimes unavailable in CI but
		// tmt's own error will be more informative. Just log a warning if neither
		// session nor system libvirt seems reachable.
		if !libvirtReachable(env) {
			slog.Warn("Could not contact libvirt (session or system); " +
				"tmt provision -h virtual may fail. Ensure libvirtd is running and your user has access.")
		}
	}

	return nil
}

// tmtBaseExecutables returns prereqs that are common to every tmt run (regardless of
// provision how).
func tmtBaseExecutables() []tmtPrereqExec {
	return []tmtPrereqExec{
		{name: "python3", label: "python3", prereq: &prereqs.PackagePrereq{
			AzureLinuxPackages: []string{"python3"},
			FedoraPackages:     []string{"python3"},
		}},
		{name: "git", label: "git", prereq: &prereqs.PackagePrereq{
			AzureLinuxPackages: []string{"git"},
			FedoraPackages:     []string{"git"},
		}},
		{name: "qemu-img", label: "qemu-img", prereq: &prereqs.PackagePrereq{
			AzureLinuxPackages: []string{"qemu-img"},
			FedoraPackages:     []string{"qemu-img"},
		}},
	}
}

// tmtBuildHeaders returns header prereqs needed to build tmt's native pip dependencies
// (independent of provision how — Python.h is needed by anything that builds C
// extensions). The Python include path is discovered from the running interpreter.
func tmtBuildHeaders(env *azldev.Env) []tmtPrereqFile {
	out := []tmtPrereqFile{
		{
			path:  "/usr/bin/gcc",
			label: "C compiler (gcc)",
			prereq: &prereqs.PackagePrereq{
				AzureLinuxPackages: []string{"gcc"},
				FedoraPackages:     []string{"gcc"},
			},
		},
		{
			path:  "/usr/bin/pkg-config",
			label: "pkg-config",
			prereq: &prereqs.PackagePrereq{
				AzureLinuxPackages: []string{"pkgconf-pkg-config"},
				FedoraPackages:     []string{"pkgconf-pkg-config"},
			},
		},
	}

	if hdr := pythonDevelHeader(env); hdr != "" {
		out = append(out, tmtPrereqFile{
			path:  hdr,
			label: "Python development headers (python3-devel)",
			prereq: &prereqs.PackagePrereq{
				AzureLinuxPackages: []string{"python3-devel"},
				FedoraPackages:     []string{"python3-devel"},
			},
		})
	}

	return out
}

// tmtVirtualExecutables returns executable prereqs specific to the `virtual` provisioner.
func tmtVirtualExecutables() []tmtPrereqExec {
	return []tmtPrereqExec{
		{name: "virsh", label: "virsh (libvirt client)", prereq: &prereqs.PackagePrereq{
			AzureLinuxPackages: []string{"libvirt-client"},
			FedoraPackages:     []string{"libvirt-client"},
		}},
		// qemu-system-x86_64 covers the vast majority of cases on x86_64 hosts. ARM
		// hosts will need qemu-system-aarch64; we can extend later as needed.
		{name: "qemu-system-x86_64", label: "qemu-system-x86_64", prereq: &prereqs.PackagePrereq{
			AzureLinuxPackages: []string{"qemu-kvm"},
			FedoraPackages:     []string{"qemu-system-x86"},
		}},
	}
}

// tmtVirtualHeaders returns header prereqs specific to the `virtual` provisioner —
// principally libvirt's pkg-config + headers needed by libvirt-python's source build.
func tmtVirtualHeaders() []tmtPrereqFile {
	return []tmtPrereqFile{
		{
			path:  "/usr/lib64/pkgconfig/libvirt.pc",
			label: "libvirt development files (libvirt-devel)",
			prereq: &prereqs.PackagePrereq{
				AzureLinuxPackages: []string{"libvirt-devel"},
				FedoraPackages:     []string{"libvirt-devel"},
			},
		},
	}
}

// pythonDevelHeader returns the absolute path to the Python.h header for the python3
// interpreter on PATH, or "" if it cannot be determined. The path is what we present
// to [prereqs.RequireFile]; if the interpreter can't be queried (very early failure),
// the caller skips the check and relies on the build-time error.
func pythonDevelHeader(env *azldev.Env) string {
	pyCmd := exec.CommandContext(env, "python3", "-c",
		`import sysconfig; print(sysconfig.get_path("include"))`)

	var stdout strings.Builder

	pyCmd.Stdout = &stdout
	pyCmd.Stderr = io.Discard

	wrapped, err := env.Command(pyCmd)
	if err != nil {
		return ""
	}

	if err := wrapped.Run(env); err != nil {
		return ""
	}

	includeDir := strings.TrimSpace(stdout.String())
	if includeDir == "" {
		return ""
	}

	return filepath.Join(includeDir, "Python.h")
}

// libvirtReachable returns true if `virsh -c qemu:///session list` or
// `virsh -c qemu:///system list` returns successfully. Best-effort; absence of `virsh`
// or any error returns false (and we log a warning rather than fail).
func libvirtReachable(ctx context.Context) bool {
	for _, uri := range []string{"qemu:///session", "qemu:///system"} {
		probeCmd := exec.CommandContext(ctx, "virsh", "-c", uri, "list")
		probeCmd.Stdout = io.Discard
		probeCmd.Stderr = io.Discard

		// We just need to know whether the command succeeds; if it does, libvirt
		// at that URI is reachable.
		if err := probeCmd.Run(); err == nil {
			return true
		}
	}

	return false
}

// Compile-time assertion that the [tmt] package's [tmt.ProvisionVirtual] equals
// the projectconfig-side enum value. If they ever diverge, this fails to compile.
//
//nolint:unused // compile-time assertion
var _ = func() bool {
	if string(tmt.ProvisionVirtual) != string(projectconfig.TmtProvisionHowVirtual) {
		panic("tmt.ProvisionVirtual and projectconfig.TmtProvisionHowVirtual disagree")
	}

	return true
}()
