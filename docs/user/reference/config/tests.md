# Tests and Test Groups

The `[tests]` and `[test-groups]` sections declare framework-agnostic test
metadata that components and images can target by name. Each test entry
binds a single test (a pytest run, a LISA case, or a TMT plan)
to a named identifier; each group entry bundles tests (and
named references) under one name so callers can reference a curated set
without enumerating every member.

## Test Definition

Each entry under `[tests.<name>]` describes one configuration of one
runner. Framework-specific options live in a subtable (`pytest`, `lisa`, `tmt`).
azldev validates and consumes the supported fields documented below. Additional
metadata may be retained for external orchestration; arbitrary keys are not
automatically forwarded as native framework options.

For complete runnable examples, see [Test a component](../../how-to/test-component.md)
and [Test an image](../../how-to/test-image.md). For the result format and exit
status contract, see [Automate artifact tests](../../how-to/automate-artifact-tests.md).

| Field | TOML Key | Type | Required | Description |
|-------|----------|------|----------|-------------|
| Type | `type` | string | Yes | Test framework: `pytest`, `lisa`, or `tmt` |
| Description | `description` | string | No | Human-readable description |
| Kind | `kind` | string | No | Test kind hint: `functional` or `performance` |
| Long running | `long-running` | boolean | No | Hints that this test may run for hours |
| Metrics enabled | `metrics-enabled` | boolean | No | Hints to the test execution environment/validation service whether metrics from this test should be collected and stored |
| Required capabilities | `required-capabilities` | string array | No | Capability tokens the image must declare for this test to be applicable |
| Execution | `execution` | table | No | Optional framework-independent strategy and named supporting image defaults |
| Lisa | `lisa` | table | When `type = "lisa"` | LISA-specific configuration (see [LISA fields](#lisa-fields)) |
| Tmt | `tmt` | table | When `type = "tmt"` | TMT plan configuration (see [TMT fields](#tmt-fields)) |
| Pytest | `pytest` | table | When `type = "pytest"` | pytest-specific configuration (see [Pytest fields](#pytest-fields)) |

Exactly one framework subtable must be present, and it must match `type`; the
other two must be absent (the loader rejects a missing or mismatched subtable).

### Execution defaults

Both test commands use the same framework adapters. CLI options override these
optional defaults; existing TOML files need no migration.

```toml
[tests.example.execution]
strategy = "vm"
base-images = { vm = "vm-base", container = "container-base" }
```

`strategy` is `vm`, `container`, `ssh`, or `host`. When omitted, LISA and TMT
default to VM, and pytest to host-side inspection. Not every framework supports
every strategy: unsupported combinations are errors, not silently skipped tests.
TMT supports all four for components, but an image test cannot use an existing
SSH target or host in place of actually consuming its subject image.

`base-images` maps `vm`/`container` to named supporting images. By default they
come from the selected distro version's
[published image catalog](distros.md#published-test-artifacts); CLI
`--base-image-source local` instead selects local output, building from a local
recipe if that output is missing.
It applies only when a component test requires a supporting image; image tests
use the image under test. CLI `--base-image` overrides the test mapping, which
overrides the distro version's `test-base-images` defaults.

Existing scheduling metadata (`required-capabilities`, `long-running`, and
`metrics-enabled`) is preserved for external orchestration. The local executor
checks concrete framework, strategy, architecture, and artifact-format support;
it does not add a new image-capability policy or infer capabilities from an
uninspected image.

### LISA Fields

The `[tests.<name>.lisa]` subtable is mostly opaque to azldev, but it
recognizes a few keys used to select LISA test cases and, optionally, to
run the test locally via `azldev image test` (booting the image in a QEMU
VM). It can also run against component packages installed into a supporting VM.

| Field | TOML Key | Type | Description |
|-------|----------|------|-------------|
| Source | `source` | table (`git-url`, `ref`) | Git source for the LISA framework; `ref` must be a full 40-character hex commit SHA. Local execution requires either this source or CLI `--lisa-dir` pointing to a framework checkout. |
| Criteria | `criteria` | table or array of tables | One or more LISA criteria blocks (`name`, `area`, `category`, `priority`, `tags`) used to select test cases. |
| Name | `name` | string | Shorthand for a single criteria with a `name` filter. |
| Testcase name | `testcase-name` | string | Shorthand for a single criteria matching one test case by name. |
| Testcase names | `testcase-names` | string array | Shorthand for a single criteria matching multiple test cases by name (joined as an OR). |
| Pip pre-install | `pip-pre-install` | string array | Pip packages to install before the framework (for overriding version pins); used only for local execution. |
| Pip extras | `pip-extras` | string array | Pip extras to install from the LISA framework package; used only for local execution. |
| Extra args | `extra-args` | string array | Additional arguments passed to LISA. Supports `{image-path}`, `{image-name}`, `{capabilities}`, `{properties}` placeholders; used only for local execution. |

At least one of `criteria`, `name`, `testcase-name`, or `testcase-names` is
required.

Local LISA runs use a private framework checkout and environment, with a
generated admin key removed at the end. RAW, qcow2, VHD, and VHDX disks are
content-detected and converted privately when necessary. The host needs a
working system libvirt/QEMU service and `ssh-keygen`. Install the extras
required by the selected framework revision and test collection; Microsoft's
collection may need `pip-extras = ["libvirt", "azure"]` even when using QEMU.
The image must support cloud-init on QEMU: Hyper-V-only telemetry configuration
can produce a genuine cloud-init error and is not silently disabled by azldev.
See the [LISA walkthrough](../../how-to/test-image.md#boot-and-inspect-the-guest-with-lisa)
for a pinned framework, a complete guest test, and a targeted QEMU-image
customization example.

### TMT Fields

The `[tests.<name>.tmt]` subtable identifies an upstream TMT plan. Both test
commands run it using the shared TMT adapter. The adapter materializes the
pinned plan repository, selects the requested native provisioner, and installs
candidate RPMs for component tests. Image tests do not inject a candidate RPM
prepare step. TMT retains control of discovery, execution, reporting, and finish.

| Field | TOML Key | Type | Description |
|-------|----------|------|-------------|
| Source | `source` | table (`git-url`, `ref`) | Git repository containing the plan. Optional; when omitted, the test source must instead be supplied directly with `--source-dir`. When present, `ref` must be a full 40-character hex commit SHA. |
| Plan | `plan` | string | Absolute TMT plan name to run. Optional; when omitted, tmt discovers and runs every enabled plan it finds (see [TMT Tests & fmf Metadata](../../explanation/tmt-tests.md)). `--execution vm` always requires an explicit plan (via this field or `--plan`), since hardware export is resolved per-plan. |
| Context | `context` | table of string arrays | Native fmf context dimensions, such as `{ distro = ["azurelinux-4"], arch = ["x86_64"] }`. Applied consistently to plan export and execution. |
| Pip extras | `pip-extras` | string array | Additional TMT extras, unioned with the provisioner's required extra. |
| Run arguments | `run-extra-args` | string array | Supported global TMT options before `run`, such as `--show-time`. |
| Plan arguments | `plan-extra-args` | string array | Supported plan options, such as `--filter`. |
| Provision arguments | `provision.extra-args` | string array | Supported native provisioner options, such as `--memory 4096` or `--ssh-option`. |
| Cloud-init commands | `cloud-init-runcmds` | string array | Additional guest first-boot commands for VM tests, before readiness probing. Not host commands. |

Guest bootstrap policy is explicit and test-scoped. The adapter does not create a
`fedora` account or inject firewall commands. For an Azure Linux image with
firewalld enabled, a test using testcloud's session connection can declare:

```toml
[tests.azurelinux-smoke.tmt]
plan = "/plans/smoke"
cloud-init-runcmds = [
  "firewall-cmd --add-service=ssh && firewall-cmd --add-port=10022/tcp",
]
```

These commands run in declaration order, before testcloud starts its readiness
service. TCP 10022 is needed by `qemu:///session`, not a universal VM requirement.
Use commands appropriate to the selected image and connection mode; associate
the named test with the relevant images/components. This field is not an
image-level default automatically inherited by unrelated tests. A TMT `prepare`
step cannot fix a port needed to complete provisioning.

**Migration:** tests that relied on azldev's previous automatic firewall
workaround must now declare their bootstrap commands. Rules without
`--permanent` last until reboot; they are not removed immediately after readiness.

Native argument lists are scope-checked. They cannot override azldev's artifact,
work-directory, report, or execution-strategy selection, or inject another TMT
step. The older `provision-extra-args` spelling is accepted as an alias;
do not supply both forms. A configured `provision.how` must agree with the
framework-independent execution strategy.

VM runs accept RAW, qcow2, VHD, and VHDX inputs. `qemu-img` detects actual
content; conversion to a private qcow2 occurs only when needed. A declared
plan user is preserved; otherwise local VM testing uses `azureuser`, rather
than assuming Azure Linux images allow root SSH. Set a different VM user in
the plan; SSH strategy uses the common `--ssh-user` option.
An explicit plan selects that exact name, not a regular expression or prefix.
VM plans with multiple provision phases are rejected rather than applying
one guest's hardware or user settings to every guest.

VM hosts need QEMU/libvirt, UEFI firmware, a cloud-init ISO tool such as
`genisoimage`, and permission to use virtualization. Installing TMT's virtual
extra may also require a C compiler, Python development headers, `pkg-config`,
and libvirt development files. These are host prerequisites, not guest packages;
native installation errors remain visible. Host and container strategies do not
require the VM stack.

TMT itself is installed from the full Git commit in
`internal/tmt/requirements.txt`, not a floating branch. Runtime setup requires
`git` and Python 3.12 or newer. The selected commit includes upstream's
one-second SSH-master polling fix; azldev no longer patches SSH polling.

For example, define a plan as follows:

```toml
[tests.example-tmt]
type = "tmt"

[tests.example-tmt.tmt]
source = { git-url = "https://example.test/tests.git", ref = "0123456789012345678901234567890123456789" }
plan = "/plans/example"
```

After associating `example-tmt` with a component, run it locally with
`azldev component test <component> --base-image-path <image.qcow2> --rpm <package.rpm>`.

Prefer `--base-image-path` over the deprecated component `--image-path` alias.
Use `--execution vm` rather than the deprecated `--provision virtual` spelling,
and `--execution host` rather than `--provision local`.

### Pytest Fields

The `[tests.<name>.pytest]` subtable configures a pytest run. azldev validates
and consumes these fields: it prepares the Python environment according to
`install` (see below), and invokes `python -m pytest` with the configured
arguments.

| Field | TOML Key | Type | Required | Description |
|-------|----------|------|----------|-------------|
| Working directory | `working-dir` | string | Yes | Source project directory, copied into a private runtime before invoking pytest. Must be non-empty. Relative paths are resolved against the config file's directory. |
| Test paths | `test-paths` | string array | Yes | Test file paths or directories passed to pytest as positional arguments. Must be a non-empty list of non-empty strings. Each entry is glob-expanded (including recursive `**`) relative to `working-dir`. Patterns that match nothing are passed through unchanged so pytest reports the failure. |
| Extra args | `extra-args` | string array | No | Additional arguments passed to pytest verbatim, after placeholder substitution (see [Placeholders](#placeholders)). |
| Install mode | `install` | string | No | How dependencies are installed into the venv. One of `pyproject`, `requirements`, or `none` (default). |
| Source | `source` | table (`git-url`, `ref`) | No | Optional pinned test repository. With a source, `working-dir` is relative to its checkout. Without one, existing config-file-relative path semantics are preserved. |

#### Install modes

| Mode | Behavior |
|------|----------|
| `pyproject` | Installs a private copy of the project in editable mode. Errors if `pyproject.toml` is not present. |
| `requirements` | Installs from the private source copy's `requirements.txt`. Errors if the file is not present. |
| `none` (default) | Uses the caller-prepared environment at `<project-work-dir>/pytest-venv/<test-name>`, preserving main's established location without creating or changing it. Both a valid venv and importable pytest are required; there is no fallback to host Python. |

azldev always requests a private native JUnit report from pytest. The shared
`--junit-xml <path>` flag additionally writes an aggregate report for all selected
tests; do not add pytest's native report flag to `extra-args`.
Relative CLI paths, including `--junit-xml`, are resolved against the selected
project directory (including `-C`), not the framework's working directory.

Absolute pytest selectors outside the configured working directory remain explicit
caller-owned paths for compatibility; they are not copied into the private source
tree. Pytest's cache and bytecode writes are kept off those source paths. Prefer
selectors within the declared source tree when a fully private source snapshot is
required.

#### Placeholders

The following placeholders may appear in pytest and LISA `extra-args` and are
substituted at run time. They are **not** substituted in `test-paths`.

| Placeholder | Substitution |
|-------------|--------------|
| `{image-path}` | Absolute path to the image artifact under test |
| `{image-name}` | Name of the image being tested |
| `{capabilities}` | Comma-separated names of enabled boolean capabilities |
| `{properties}` | JSON object containing the image's string properties |
| `{inputs-path}` | Pytest: path to the JSON artifact manifest (also in `AZLDEV_TEST_INPUTS`) |
| `{subject-name}` | Pytest: component or image name under test |

## Test Group

Each entry under `[test-groups.<name>]` names an ordered list of test
references that callers can target as a single unit.

| Field | TOML Key | Type | Required | Description |
|-------|----------|------|----------|-------------|
| Description | `description` | string | No | Human-readable description |
| Tests | `tests` | array of [TestRef](#test-reference) | No | Ordered members of the group (name refs only) |

## Test Reference

`TestRef` is an inline table with exactly one of `name` or `group`:

| Field | TOML Key | Type | Description |
|-------|----------|------|-------------|
| Name | `name` | string | References a `[tests.<name>]` entry |
| Group | `group` | string | References a `[test-groups.<name>]` entry |
| SKU groups | `sku-groups` | string array | **Image test references only.** References one or more [`[sku-groups.<name>]`](#sku-groups) entries; external orchestration fans the referenced test or group out once per Azure VM size across every listed SKU group. Entries must be unique. Rejected on component tests and on `[test-groups]` members. |

## Referencing from Components and Images

Components and images both expose a `tests` subtable that holds a list
of `TestRef`s:

```toml
[components.kernel.tests]
tests = [{ group = "kernel-bvt" }, { name = "kdump-smoke" }]

[images.vm-base.tests]
tests = [{ group = "bvt" }]
```

An **image** reference may additionally carry `sku-groups` to fan the test or
group out across the VM sizes of one or more [SKU groups](#sku-groups):

```toml
[images.marketplace-gen2.tests]
tests = [{ group = "multi-sku-tests", sku-groups = ["multi-sku-amd64", "multi-sku-arm64"] }]
```

`sku-groups` is only valid on image test references; it is rejected on component
tests and on `[test-groups]` members.

## SKU Groups

`[sku-groups.<name>]` defines a named list of Azure VM sizes used to fan out an
image's tests across multiple SKUs. External test orchestration runs the
referenced test (or test group) once per VM size. SKU groups are referenced only
from **image** test references via the [`sku-groups`](#test-reference) field.

| Field | TOML Key | Type | Required | Description |
|-------|----------|------|----------|-------------|
| Description | `description` | string | No | Human-readable description |
| Arch | `arch` | string | Yes | CPU architecture of every VM size in the group; one of `amd64` or `arm64`. These map to the image [`architectures`](images.md) values `x86_64` and `aarch64` respectively (SKU groups use the Azure/Go `GOARCH` tokens, images use the kernel `uname` names). azldev rejects a SKU group whose arch is not supported by an **arch-restricted** image that references it. |
| VM sizes | `vm-sizes` | string array | Yes | Azure VM size names (e.g. `Standard_D4s_v5`). Must be non-empty with no duplicate entries. |

SKU group names must be unique across all config files; a duplicate name is an
error.

```toml
[sku-groups.multi-sku-amd64]
description = "x86-64 VM sizes for multi-SKU coverage"
arch = "amd64"
vm-sizes = ["Standard_D4s_v5", "Standard_L8s_v3"]
```

> **Note:** A `sku-groups` entry cannot be mapped to arbitrary LISA tests. LISA tests
> declare their own requirements (CPU count, memory, features such as NVMe, etc.)
> and LISA skips a test when the SKU it runs on does not satisfy those
> requirements. Fanning a normal test out across a SKU group therefore just
> produces skips on SKUs that do not match. SKU mapping is only meaningful for
> the special case of multi-SKU performance tests, which are written to run
> across the SKUs in a group and report per-SKU results.

## VM SKUs

`[vm-skus.<vm-size>]` carries free-form per-VM-size metadata consumed by external
test orchestration to resolve test parameters; azldev does not interpret the
values. Each VM size may be defined only once across all config files.

```toml
[vm-skus.Standard_D4s_v5]
vcpus = 4
```

## Example

```toml
[tests.bvt-ssh]
type        = "pytest"
description = "Basic SSH boot verification"
kind        = "functional"
required-capabilities = ["ssh"]
pytest = { working-dir = "tests/bvt", test-paths = ["test_ssh.py"] }

[tests.kdump-smoke]
type        = "lisa"
description = "Smoke test for kdump"
lisa        = { testcase-name = "kdump_smoke" }

[tests.kdump-smoke-local]
type        = "lisa"
description = "Smoke test for kdump (runs locally via 'azldev image test')"
lisa.source = { git-url = "https://github.com/microsoft/lisa.git", ref = "0123456789012345678901234567890123456789" }
lisa.testcase-names = ["kdump_smoke"]

[test-groups.bvt]
description = "Build verification tests"
tests = [
  { name  = "bvt-ssh" },
  { name  = "kdump-smoke" },
]
```

## Migrating from `[test-suites]`

The legacy `[test-suites]` section and the `tests.test-suites` image references
have been removed. Existing configuration must be migrated to the `[tests]` /
`[test-groups]` shape described above. Section renames are mechanical, but the
new `[tests.NAME.pytest]` validation is stricter than legacy `[test-suites]` —
see [Pytest field requirements](#pytest-field-requirements) below.

### Section and reference renames

| Legacy | Replacement |
|--------|-------------|
| `[test-suites.NAME]` | `[tests.NAME]` |
| `[test-suites.NAME.pytest]` | `[tests.NAME.pytest]` |
| `[test-suites.NAME.lisa]` | `[tests.NAME.lisa]` |
| `[images.NAME.tests]` `test-suites = [{ name = "x" }]` | `[images.NAME.tests]` `tests = [{ name = "x" }]` |

### Pytest field requirements

The pytest keys keep the same names, but `[tests.NAME.pytest]` validation is
stricter than legacy `[test-suites]`: `working-dir` and `test-paths` are now
**required** and must be non-empty. Legacy suites that omitted `test-paths`, or
that relied on `install = "none"` without a `working-dir`, must add these fields
when migrating — renaming the section alone is not enough.

| Field | Legacy `[test-suites]` | New `[tests]` |
|-------|------------------------|---------------|
| `working-dir` | Optional (required only for `install = "pyproject"`/`"requirements"`) | **Required** (non-empty string) |
| `test-paths` | Optional | **Required** (non-empty list of non-empty strings) |
| `extra-args`, `install` | Optional | Optional (unchanged) |

### LISA field changes

The LISA subtable changed how the framework source and test cases are
expressed:

| Legacy (`[test-suites.NAME.lisa]`) | Replacement (`[tests.NAME.lisa]`) |
|------------------------------------|-----------------------------------|
| `[...lisa.framework]` table (`git-url`, `ref`) | `source = { git-url, ref }` (or a `[...lisa.source]` subtable) |
| `test-cases = ["a", "b"]` | `testcase-names = ["a", "b"]` (or `testcase-name`, `name`, or a `criteria` block — see [LISA fields](#lisa-fields)) |
| `pip-pre-install`, `pip-extras`, `extra-args` | unchanged |

### Before and after

```toml
# Before (removed):
[test-suites.smoke]
type = "pytest"
[test-suites.smoke.pytest]
working-dir = "tests/smoke"
test-paths = ["cases/test_*.py"]

[test-suites.vm-integration]
type = "lisa"
[test-suites.vm-integration.lisa]
test-cases = ["verify_grub"]
[test-suites.vm-integration.lisa.framework]
git-url = "https://github.com/microsoft/lisa.git"
ref = "0123456789012345678901234567890123456789"

[images.vm-base.tests]
test-suites = [{ name = "smoke" }, { name = "vm-integration" }]
```

```toml
# After:
[tests.smoke]
type = "pytest"
[tests.smoke.pytest]
working-dir = "tests/smoke"
test-paths = ["cases/test_*.py"]

[tests.vm-integration]
type = "lisa"
[tests.vm-integration.lisa]
testcase-names = ["verify_grub"]
[tests.vm-integration.lisa.source]
git-url = "https://github.com/microsoft/lisa.git"
ref = "0123456789012345678901234567890123456789"

[images.vm-base.tests]
tests = [{ name = "smoke" }, { name = "vm-integration" }]
```

## Related Resources

- [Components](components.md#component-tests) — per-component `tests` field
- [Images](images.md#image-tests) — per-image `tests` field
- [Config File Structure](config-file.md) — top-level config layout
