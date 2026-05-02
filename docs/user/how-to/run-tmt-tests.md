# How To: Run tmt Tests Against an Image

This guide walks through configuring and running [tmt](https://tmt.readthedocs.io/) (Fedora's Test Management Tool) plans against an Azure Linux image. tmt plans live in dist-git (or any FMF-shaped git repository); azldev clones the source, sets up an isolated Python venv, installs tmt with the appropriate provision plugin, and invokes `tmt run` against the image you point at.

## Prerequisites

- A configured project with at least one buildable image (see [Build an Image](build-image.md)).
- A built image artifact (raw, qcow2, VHD, or VHDX). azldev will convert non-qcow2 images automatically before handing them to tmt.
- The image must support cloud-init (or another mechanism testcloud can use to inject SSH keys). Most VM-bootable Azure Linux images include cloud-init by default.
- A running libvirt daemon with access for the invoking user (`qemu:///session` or `qemu:///system`).

azldev's preflight checks for the host packages required to install tmt's `provision-virtual` extra and offers to install missing ones if the runtime policy permits. The set covers:

| Need | Provided by |
|---|---|
| C compiler | `gcc` |
| pkg-config | `pkgconf-pkg-config` |
| libvirt headers (for `libvirt-python` source build) | `libvirt-devel` |
| Python headers (for `libvirt-python` source build) | `python3-devel` |
| libvirt client tooling | `libvirt-client` |
| QEMU system emulator | `qemu-system-x86` (Fedora) / `qemu-kvm` (Azure Linux) |

If you prefer to install them manually:

```bash
# Fedora
sudo dnf install -y gcc pkgconf-pkg-config libvirt-devel python3-devel libvirt-client qemu-system-x86

# Azure Linux
sudo tdnf install -y gcc pkgconf-pkg-config libvirt-devel python3-devel libvirt-client qemu-kvm
```

## Configure a Test Suite

Add a `[test-suites.NAME]` block of `type = "tmt"` and reference it from the image:

```toml
[test-suites.bash-fedora-shell]
type = "tmt"
description = "Fedora bash dist-git tmt smoke (plans/shell)"

  [test-suites.bash-fedora-shell.tmt]
  source = { git-url = "https://src.fedoraproject.org/rpms/bash.git", ref = "a6bcc6767229199f4f02b781d1d39df0835d894b" }
  plan   = "/plans/shell"

    [test-suites.bash-fedora-shell.tmt.provision]
    how = "virtual"

[images.vm-base]
description = "VM Base Image"
definition = { type = "kiwi", path = "vm-base/vm-base.kiwi" }
tests = { test-suites = [{ name = "bash-fedora-shell" }] }
```

### Required fields

| Field | Notes |
|---|---|
| `source.git-url` | Any git URL — Fedora dist-git, CentOS Stream dist-git, an internal repo, etc. |
| `source.ref` | **Must be a 40-character hex commit SHA.** Branch names and tags are rejected so that runs are reproducible. |
| `plan` | A tmt plan name. **MVP requires exactly one plan.** Plan names start with `/`; the on-disk path strips the leading slash. |

### Optional fields

| Field | Purpose |
|---|---|
| `pip-extras` | Additional pip extras to install alongside tmt. The extra required by `provision.how` is auto-derived (e.g., `provision-virtual` for `how = "virtual"`); user entries are unioned on top. |
| `context` | tmt context dimensions (`tmt -c k=v ...`). Multi-valued: `context = { distro = ["fedora-rawhide"], arch = ["x86_64", "aarch64"] }`. Used by plans' `adjust:` rules. |
| `run-extra-args` | Verbatim args inserted before `tmt run`. Cannot include `--id`, `-c`, or `--context` (managed by azldev). |
| `plan-extra-args` | Verbatim args inserted after `plan -n <plan>`. |
| `provision-extra-args` | Verbatim args inserted after `provision -h <how>`. Cannot include `--image` (managed by azldev — see below). |
| `provision.how` | Optional. MVP supports `"virtual"` only. If unset, tmt's own default applies. |

### Why `image` is not configurable

The image-under-test is the whole point of `azldev image test`. The runner constructs `--image <PATH>` from the image artifact azldev resolves (or `--image-path` flag override) and refuses to honor a user-supplied `--image` in `provision-extra-args` to keep the contract clear: **one invocation tests one image, decided by the image-test command, not the suite config.**

## Run the Suite

```bash
azldev image test vm-base
# or with an explicit image path
azldev image test vm-base --image-path ./out/images/vm-base/azl4-vm-base.x86_64-0.1.raw
# or generate JUnit output (single-suite invocations only)
azldev image test vm-base --junit-xml results.xml
```

Each invocation gets a fresh, uniquely-named directory:

```
<work-dir>/tmt/runs/<suite-name>/<UTC-timestamp>-<rand>/
├── plans/<plan-path>/
│   ├── prepare/results.yaml
│   ├── execute/results.yaml         # tmt's canonical output
│   └── …
├── azldev-results.json              # always emitted by azldev
├── junit.xml                        # only when --junit-xml requested
└── image.qcow2                      # converted image, when source wasn't already qcow2
```

The runner prints the run directory path at start and finish. Any failure references it explicitly.

## Result Artifacts

`azldev-results.json` is the structured-results contract:

```json
[
  {
    "name": "/path/to/test",
    "status": "pass",
    "durationSeconds": 3.2,
    "outputPath": "/path/to/run-dir/.../execute/data/test/output.txt"
  }
]
```

An empty array means tmt didn't get far enough to run any test (most commonly a provisioning failure). The run directory will still contain provision-step logs and a serial-console capture (see `plans/<plan>/provision/<guest>/console.txt`).

## Tunables

Environment variables that influence runs:

| Variable | Default | Purpose |
|---|---|---|
| `TMT_BOOT_TIMEOUT` | 300 (set by azldev) | Seconds testcloud waits for the guest to come up. azldev raises it from testcloud's own 120-second default; you can override in your shell if a particular image needs longer. |

## Troubleshooting

- **Boot timeout (`failed to boot in N seconds`)**: testcloud could not establish SSH to the guest. Inspect the serial console at `<run-dir>/plans/<plan>/provision/default-0/console.txt` (or the testcloud-managed VM in `~/.cache/testcloud/`). Common causes: image lacks cloud-init or it isn't enabled at boot, virtio drivers missing from the kernel, firmware mismatch (UEFI vs BIOS).
- **`libvirt-python` build failure during pip install**: missing host package(s). Check the preflight error message for the specific file/executable; install the suggested package and rerun.
- **`run dir … already exists; refusing to reuse`**: should never happen in normal usage (run-ids are timestamp + random hex). If you see it, your clock skewed backwards or you ran the same suite within the same second; just rerun.
- **`forbidden extra arg`**: you put a flag azldev manages itself (`--id`, `--image`, `-c`, `--context`) inside one of the `*-extra-args` lists. Move the value to the appropriate first-class field (`context`) or remove it.

## What's Not in the MVP

- Per-component opt-in (e.g., a `[components.NAME.tests.tmt]` block that synthesizes a suite from the component's upstream metadata).
- Project-level `[tmt-config.<preset>]` blocks for shared runtime knobs.
- Provision modes other than `virtual` (e.g., `container`, `connect`, an azldev-qemu plugin).
- Multi-plan invocations and multi-suite JUnit merging.
- Cross-runner table/JSON output of structured results.
- Injecting locally-built RPMs into the guest before testing.

These are tracked in the project's tmt-integration plan.
