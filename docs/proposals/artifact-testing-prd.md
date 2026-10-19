# PRD: Consistent testing of Azure Linux build artifacts

**Status:** Draft for stakeholder review.
**Baseline:** `upstream/main` at `617a688`; proposal reconstructed from
`feat/shared-test-execution` at `0db23ee`.
**Scope:** Customer-visible CLI behavior, configuration, workflows, and results.

## 1. Unmet customer requirements

Customers need to test the packages and images they produce without rebuilding
their workflow around each test framework. Existing commands provide useful
pieces, but do not yet provide one consistent product experience.

| ID | Customer requirement | Gap in the baseline |
|----|----------------------|---------------------|
| U1 | As a package or image maintainer, run the tests appropriate to my artifact using a consistent command. | Component testing runs TMT; image testing runs LISA and pytest, but rejects local TMT execution. The command surfaces differ. |
| U2 | As a package maintainer, test my changed RPMs on a known-good supporting image without manually locating every input. | Component testing requires explicit RPMs and, for VM execution, an explicit image file. It has no common local/build/published artifact-selection policy. |
| U3 | As an image maintainer, choose deliberately between existing output, a new build, a published image, and a file supplied by someone else. | Image testing can find existing local output or take a path, but does not offer the proposed reuse-or-build and published-artifact choices. |
| U4 | As a consumer of published artifacts, run tests without owning the producer's build recipe or packaging sources. | There is no distro-version catalog of published test inputs, and component selection can require packaging-lock consistency even for supplied RPMs. |
| U5 | As a test author, reuse a test across suitable environments without embedding every image-specific setup requirement in that test. | Supporting-image selection and guest bootstrap policy are not modeled consistently. Some guest accommodations are implicit. |
| U6 | As a developer or automation client, find the configured tests without triggering downloads, builds, or framework execution. | There is no shared, configuration-only test-listing command. |
| U7 | As a CI owner, distinguish assertion failures from setup errors, skips, and cancellation, and retain useful reports across frameworks. | Existing reporting and execution behavior differ between the component and image commands; there is no common artifact-test result contract. |
| U8 | As a developer iterating on code or tests, understand what ran and preserve my original files and caller-owned machines. | Input identity, runtime ownership, source overrides, and retained evidence need consistent guarantees across the supported workflows. |

These are requirements inferred from the tool's workflows, not claims about
measured customer adoption or survey results.

## 2. Proposed product outcome

Introduce a shared `azldev test` command family:

```text
azldev test list [--component NAME | --image NAME] [--test NAME_OR_GROUP]...
azldev test run  (--component NAME | --image NAME) [--test NAME_OR_GROUP]... [options]
```

A customer should be able to choose **what to test**, **which tests to run**,
**where they execute**, and **where the input artifacts come from** independently,
subject to the framework's supported combinations.

The existing `[tests]`, `[test-groups]`, and component/image test associations
remain the foundation. They are not new schema concepts. The proposal expands
their execution support and adds configuration for artifact acquisition,
execution defaults, and named-image bootstrap policy.

This is not a replacement language for writing native tests. TMT plans, LISA
cases, and pytest tests continue to express their own assertions and native
requirements.

## 3. Customer model

| Concept | Meaning | How the customer selects it |
|---------|---------|-----------------------------|
| Subject | The component's binary packages or the image being evaluated | `--component NAME` or `--image NAME` |
| Named test | One configured framework invocation, potentially containing many native cases | A `[tests.NAME]` entry; selected directly or through a group |
| Framework | TMT, LISA, or pytest | The test's existing `type` field |
| Execution strategy | A managed VM, managed container, existing SSH target, or host-side execution | Test defaults or `--execution` |
| Artifact origin | Local build output, a published LKG artifact, or an explicit file | Artifact-selection flags |
| Supporting image | The VM/container image used to exercise a component's packages | Base-image defaults or flags; not a second input to image-subject testing |
| Test source | The repository or local tree containing the tests/framework | Framework configuration, `--source-dir`, or `--lisa-dir` |
| Image identity | A logical image name within the selected distro/version | For example, `vm-base`, regardless of artifact origin |

**LKG** means the artifact advertised as last-known-good by the configured
publisher. azldev consumes that declaration; it does not certify the artifact,
discover an organization's cloud endpoints, or decide which build should be LKG.

The host running a framework is not necessarily the system being tested.
For example, pytest inspects an artifact on the host, while LISA runs on the host
and boots a guest to execute its checks.

## 4. Proposed CLI contract

### 4.1 List configured tests

```sh
azldev test list
azldev test list --component curl
azldev test list --image vm-base
azldev test list --test regression -O json
```

Without a subject, list the project's named test catalog. With a subject, list
its associated tests after expanding groups and removing duplicates. Rows expose
the test name, framework, description when present, and default strategy.

Explicit `--test` selectors replace the subject's associations for that
invocation. Unknown or ambiguous selectors are errors. A known subject or group
with no tests returns an empty successful list.

Listing must not fetch test sources, install frameworks, acquire artifacts, or
execute framework code. It must work without packaging-lock consistency or a
prepared test runtime. Normal project/configuration validation still applies.

This is **catalog enumeration**, not native case discovery. Listing
`kernel-regression` does not enumerate every case its framework will collect,
nor prove that its prerequisites are available. No `discover` subcommand or
expensive-discovery option is included in this proposal.

### 4.2 Run selected tests

`run` requires exactly one nonempty subject selector. With no `--test`, run the
subject's associated tests. With explicit selectors, use the named tests/groups
from the project catalog instead. Deduplicate selected tests and reject unknown
selectors even if another selector is valid.

Execution requires a configured project and a selected, defined default
distro/version, including when artifact files are supplied explicitly. That
provides test context; it does not require a local image build recipe.

An empty execution selection is an error, unlike an empty listing. All selected
named tests are attempted by default; `--fail-fast` prevents subsequent named
tests from starting after a failure.

The shared options are:

| Purpose | Options | Proposed behavior |
|---------|---------|-------------------|
| Choose subject/tests | `--component`, `--image`, `--test` | One subject; repeatable test/group selectors |
| Choose subject artifacts | `--artifact-source local\|lkg\|path`, `--rebuild`, `--rpm`, `--image-path` | Select the candidate bytes independently of any supporting image |
| Choose component base image | `--base-image`, `--base-image-source local\|lkg\|path`, `--base-image-path`, `--rebuild-base-image` | Select a supporting image only when the selected component tests consume one |
| Choose target execution | `--execution vm\|container\|ssh\|host`, `--arch` | Override configured execution; architecture defaults to the host and accepts `x86_64` or `aarch64` |
| Use an existing SSH target | `--ssh-host`, `--ssh-user`, `--ssh-key`, `--ssh-port` | TMT component tests using `ssh`; port defaults to 22 |
| Iterate on test sources | `--source-dir`, `--lisa-dir`, `--plan` | Override TMT/pytest sources, the LISA framework checkout, or the exact TMT plan |
| Control output/execution | `--work-dir`, `--junit-xml`, `--fail-fast`, global `--dry-run` | Isolated run output, aggregate JUnit, stop-starting policy, and planning without execution |

In the shared command, `--image-path` always means the image **subject** and
requires `--image`. A component's supporting image uses `--base-image-path`.
`--rpm` requires a component subject.

Framework-specific overrides apply only to matching selected tests. An override
with no selected consumer is an error. A global strategy override must be
supported by every selected test; it must not silently exclude incompatible tests.

Relative test-command path flags resolve against the selected project directory,
including when using `-C`. `--work-dir` defaults to the project's configured work
directory. Source-override and work directories must not overlap.

`--dry-run` reports planned work without executing tests or producing persisted
test reports. It does not prove that an image boots, dependencies can be
installed, or native cases can be collected.

### 4.3 Supported execution combinations

| Framework | Component subject | Image subject | Default strategy |
|-----------|-------------------|---------------|------------------|
| TMT | VM, container, SSH, host | VM, container | VM |
| LISA | VM | VM | VM |
| pytest | Host-side artifact inspection | Host-side artifact inspection | Host |

Image testing must consume the selected image, rather than substituting an
unrelated existing host or SSH machine. Pytest inspection is not a boot test.

VM testing accepts supported RAW, qcow2, VHD, and VHDX inputs, with any needed
conversion kept separate from the original artifact. Container tests consume
an OCI registry image or a local OCI archive, not a VM disk.

Unsupported framework/strategy/format selections must fail before artifact
acquisition or framework setup when the incompatibility is knowable from the
selection. A mixed image test run needs an input format compatible with all
selected tests; otherwise the customer must split it into separate runs.

## 5. Artifact-selection policy

### 5.1 Defaults and explicit choices

| Input | Default | Alternatives |
|-------|---------|--------------|
| Component subject RPMs | Reuse local binary build output; build if missing | `--rebuild`, `--artifact-source lkg`, or explicit `--rpm` paths/globs |
| Image subject | Reuse local image output; build if missing | `--rebuild`, `--artifact-source lkg`, or `--image-path` |
| Supporting VM/container image for component tests | Use a named published LKG image | `--base-image-source local` to reuse/build locally, `--rebuild-base-image`, or `--base-image-path` |

For a component's supporting image, name selection is:

1. Explicit `--base-image`.
2. The selected test's `execution.base-images` entry for the strategy.
3. The selected distro version's `test-base-images` entry.

An explicit base-image file without `--base-image` is anonymous and bypasses
that name lookup. Strategies that do not consume an image need no base image.

An explicit file selects those bytes without implicitly building or downloading
that input. It does **not** make the entire run offline: framework installation,
test-source retrieval, and guest test dependencies may still require networking.

### 5.2 Predictability requirements

- Subject and supporting-image choices are independent. Rebuilding a package
  must not imply rebuilding its base image.
- Local reuse does not mean "fresh relative to the current sources." Report
  available age/provenance, and let the customer request a rebuild.
- Reuse the selected component build's binary output set, not an arbitrary
  filename match or a mixture of unrelated builds. Older output layouts may be
  used when their build identity can be established unambiguously.
- Published inputs must come from the selected project distro/version catalog,
  not implicitly from a package's upstream source distro.
- Consuming a published image does not require a project-local build recipe.
  Building one locally does; azldev must not silently clone another project.
- A failed local build must not fall back to LKG. An unavailable published
  artifact must not silently become a local build.
- Reject explicit paths combined with rebuilding or LKG for that same input.
  `path` selection requires a path. Other combinations may remain independent.
- Verify configured download checksums and retain the identity of consumed
  files/OCI images. A mutable URL or tag must not make a completed run's input
  identity ambiguous.

Live component tests install all selected candidate RPMs, including replacement
of a same-version installed candidate. Pytest receives package inputs for
inspection rather than installing them on the host. Automatic conflict-aware
subpackage selection is deferred: customers must use an explicit compatible
`--rpm` subset when the build contains mutually conflicting packages.

## 6. Motivating customer workflows

Examples assume a configured project with the named tests and subjects, compatible
artifacts, and the required host/guest prerequisites. The linked walkthroughs
provide complete first-run projects and native test files.

### 6.1 Package iteration against a known-good environment

A maintainer wants to exercise changes to `curl` without changing the base OS at
the same time:

```sh
azldev test list --component curl
azldev test run --component curl
azldev test run --component curl --rebuild
```

The first run reuses existing RPMs or builds them if missing. The second
execution forces a new package build. Both use the configured LKG supporting
image when their selected tests need one.

To compare with the publisher's approved package build while retaining the same
tests and supporting-image policy:

```sh
azldev test run --component curl --artifact-source lkg
```

To deliberately change the supporting image instead:

```sh
azldev test run --component curl --base-image vm-base --base-image-source local
azldev test run --component curl --base-image vm-base --rebuild-base-image
```

The first reuses/builds the local base; the second forces its rebuild. Neither
choice changes the subject RPM policy.

### 6.2 Test artifacts received from another build system

A developer has a candidate RPM and VM image but does not need azldev to produce
either:

```sh
azldev test run --component zlib-ng \
  --rpm ./artifacts/zlib-ng-compat.rpm \
  --base-image-path ./artifacts/base.qcow2 \
  --source-dir ./tests/zlib --plan /plans/smoke
```

The project still names the component and provides the needed test configuration.
No dummy packaging spec or lock file should be required just to select those
existing artifacts. This workflow uses normal mode, not `--without-lockfile`.

The base file above is anonymous. Adding `--base-image vm-base` explicitly says
that it is a candidate for that logical image and opts into its image defaults.
See [Test a component](../user/how-to/test-component.md) for a complete example.

### 6.3 Qualify an image using different kinds of checks

An image maintainer wants both artifact inspection and live guest tests:

```sh
azldev test run --image vm-base --image-path ./candidate.qcow2 \
  --test image-inspection --test lisa-smoke --test image-tmt \
  --junit-xml ./reports/image-tests.xml
```

The test definitions choose their frameworks. Pytest inspects the artifact;
LISA and TMT boot managed guests. Do not add a global `--execution vm` to this
mixed run, because pytest's strategy is `host`.

The same tests can target different artifact origins:

```sh
azldev test run --image vm-base
azldev test run --image vm-base --rebuild
azldev test run --image vm-base --artifact-source lkg
```

These mean local reuse-or-build, forced local build, and published image,
respectively. A catalog-only image with no local recipe must use LKG or an
explicit file when no usable local output exists.
See [Test an image](../user/how-to/test-image.md) for complete pytest and LISA examples.

### 6.4 Use a suitable container or designated SSH machine

For a TMT component test that supports these environments:

```sh
azldev test run --component curl --test curl-smoke \
  --execution container --base-image container-base

azldev test run --component curl --test curl-smoke \
  --execution ssh --ssh-host disposable-vm --ssh-user tester \
  --ssh-key ./id_test --ssh-port 2222
```

The container is managed for the test. The SSH machine is caller-owned: it can
be modified by package installation and test preparation, but azldev must not
destroy it.

`--execution host` is also available for suitable TMT component tests on an
Azure Linux 4 host with sudo. It modifies the actual execution host; it is not
an isolation feature. Use only designated disposable test systems.

### 6.5 Iterate on tests without changing artifact origin

```sh
azldev test run --component curl --source-dir ./tests/curl --plan /plans/smoke
azldev test run --image vm-base --test lisa-smoke --lisa-dir ./lisa
```

These change the test/framework sources, not which candidate packages or image
are selected. Local uncommitted changes are included in private source copies.
Configured Git sources remain pinned to full commits for reproducible runs.

### 6.6 Run in CI and preserve evidence on failure

Use a unique absolute output directory for each invocation:

```sh
azldev -C /workspace/project test run --image vm-base \
  --image-path /workspace/artifacts/candidate.qcow2 \
  --work-dir /ci/artifacts/run-123/work \
  --junit-xml /ci/artifacts/run-123/junit.xml
```

CI must preserve the process exit status and publish the output directory even
on failure. Read the retained `results.json`, not a mixture of native console
output and a trailing JSON result. A unique work root prevents accidentally
reading an earlier invocation's report.
See [Automate artifact tests](../user/how-to/automate-artifact-tests.md).

## 7. Proposed configuration changes

### 7.1 Schema delta

| Configuration surface | Change |
|-----------------------|--------|
| `[tests.NAME]`, framework `type` and subtables | Existing; retain the catalog and framework-native configuration |
| `[test-groups.NAME]` and component/image `tests.tests` references | Existing; use consistently in listing and execution |
| `[tests.NAME.execution]` | Add `strategy` and strategy-to-image-name `base-images` defaults |
| `[distros.D.versions.V.images.NAME]` | Add published image variants indexed by architecture, plus optional image test defaults |
| `[distros.D.versions.V.test-base-images]` | Add default supporting image names for `vm` and `container` |
| `[distros.D.versions.V.test-packages.COMPONENT]` | Add complete published binary RPM sets indexed by architecture |
| `[images.NAME.test-defaults.tmt]` | Add project overrides for the named image's TMT VM bootstrap |
| `[tests.NAME.pytest].source` | Add a pinned Git source, alongside existing local working-directory support |
| `[tests.NAME.tmt]` | Add recognized context, runtime-extra, scoped-native-option, and guest-bootstrap settings |

The new fields are additive. Merely loading additional framework metadata does
not mean arbitrary keys become native command-line arguments.

### 7.2 Published inputs and execution defaults

The following example connects the new fields to existing test associations.
URLs and the full commit SHA are illustrative placeholders, not usable release
endpoints. The bootstrap commands assume a firewalld-managed image using
testcloud's session connection; they are not universal defaults.

```toml
[project]
default-distro = { name = "azurelinux", version = "4.0" }
work-dir = "work"
output-dir = "out"
log-dir = "logs"

[distros.azurelinux.versions."4.0"]
release-ver = "4.0"
test-base-images = { vm = "vm-base", container = "container-base" }

[distros.azurelinux.versions."4.0".images.vm-base.architectures]
x86_64 = [{ format = "qcow2", uri = "https://example.test/lkg/vm-base.x86_64.qcow2" }]
aarch64 = [{ format = "qcow2", uri = "https://example.test/lkg/vm-base.aarch64.qcow2" }]

[distros.azurelinux.versions."4.0".images.vm-base.test-defaults.tmt]
cloud-init-runcmds = [
  "firewall-cmd --add-service=ssh && firewall-cmd --add-port=10022/tcp",
]

[distros.azurelinux.versions."4.0".images.container-base.architectures]
x86_64 = [{ format = "oci", uri = "docker://registry.example.test/azurelinux/base:4.0" }]

[distros.azurelinux.versions."4.0".test-packages.curl.architectures]
x86_64 = [
  { uri = "https://example.test/builds/123/curl.rpm" },
  { uri = "https://example.test/builds/123/libcurl.rpm" },
]

[tests.curl-smoke]
type = "tmt"

[tests.curl-smoke.execution]
strategy = "vm"
base-images = { vm = "vm-base", container = "container-base" }

[tests.curl-smoke.tmt]
source = { git-url = "https://example.test/curl-tests.git", ref = "0123456789abcdef0123456789abcdef01234567" }
plan = "/plans/smoke"

[test-groups.component-regression]
tests = [{ name = "curl-smoke" }]

[components.curl.tests]
tests = [{ group = "component-regression" }]
```

Each published image architecture has a nonempty array of variants, with at most
one entry per format: `qcow2`, `raw`, `vhd`, `vhdx`, or `oci`. File downloads use
HTTP(S); OCI registry references use `docker://`. A file-download entry may add
`sha256`; a pinned OCI digest belongs in the URI instead. Published RPM entries
contain `uri` and optional `sha256`, and must describe the intended complete
component build for the selected architecture.

Missing architecture/format variants are errors, not permission to substitute a
different architecture or origin. The catalog does not require a local image
build definition.

### 7.3 One image identity, independent of artifact origin

Within the selected distro/version, the same name in the published catalog and
the project's `[images]` section identifies **one logical image**. A local
definition can add a build recipe or test policy; it is not a competing namespace.
An unrelated image must use a different name.

For managed TMT VMs, resolve `cloud-init-runcmds` in this order:

1. The distro version's named image defaults.
2. Explicit project defaults for that same image name.
3. Explicit settings on the selected test.

An omitted command list inherits. A supplied list replaces the preceding list;
lists are never concatenated. An explicit `[]` disables inherited commands.

For example, this project setting deliberately disables the catalog's commands:

```toml
[images.vm-base.test-defaults.tmt]
cloud-init-runcmds = []
```

It applies whether the named image comes from a local build, LKG, or an
explicitly associated file. It needs no local build recipe. A test can override
it again through `[tests.NAME.tmt].cloud-init-runcmds`.
Clear these commands only when the image already meets provisioning requirements;
an empty list does not bypass readiness checks.

This policy must be visible in diagnostics and retained with the run. Changing
`--artifact-source` or `--base-image-source` must not silently change test policy
or introduce fallback between artifact origins.

For component tests, `--base-image-path FILE` alone has no image identity.
`--base-image vm-base --base-image-path FILE` explicitly associates the file.
An image subject is already explicitly named by
`--image vm-base --image-path FILE`.

Bootstrap commands run in the disposable TMT guest before readiness checks,
not on the host and not by modifying the original disk image. They do not apply
to LISA, pytest, or TMT container/SSH/host execution. azldev must not silently
create a `fedora` account or open guest firewall ports.

### 7.4 Test-source and framework configuration

Existing LISA source/criteria settings and pytest installation modes remain
usable. LISA definitions can now also test a component's candidate packages in
a supporting VM. A configured LISA source or explicit `--lisa-dir` remains
necessary for local execution.

Pytest gains an optional pinned source:

```toml
[tests.image-inspection]
type = "pytest"

[tests.image-inspection.pytest]
source = { git-url = "https://example.test/image-checks.git", ref = "0123456789abcdef0123456789abcdef01234567" }
working-dir = "checks"
test-paths = ["test_image.py"]
install = "requirements"

[images.vm-base.tests]
tests = [{ name = "image-inspection" }]
```

With `pytest.source`, `working-dir` is relative to its checkout. Without it,
retain the existing config-file-relative directory semantics. `requirements`
and `pyproject` prepare dependencies from private source copies. Existing
`install = "none"` remains the default and requires the caller-prepared
`<project-work-dir>/pytest-venv/<test-name>` environment; it is not a request to
fall back to system Python.

Pytest can read the selected image/package inputs through `AZLDEV_TEST_INPUTS`
or the new `{inputs-path}` placeholder; `{subject-name}` identifies the subject.
Existing image-related placeholders remain supported. File inputs expose a path;
OCI store inputs expose an image identity instead.

For TMT, recognize these additional settings:

| Field under `[tests.NAME.tmt]` | Customer purpose |
|-------------------------------|------------------|
| `context` | Supply native fmf dimensions, such as distro and architecture, as string arrays |
| `pip-extras` | Request additional TMT runtime extras |
| `run-extra-args` | Supply supported global options before native `run`, such as `--show-time` |
| `plan-extra-args` | Supply supported native plan options |
| `provision.extra-args` | Supply supported provisioner options, such as VM memory |
| `cloud-init-runcmds` | Override guest first-boot commands for the named test |

Native options must not override managed artifact, execution, work-directory,
or reporting choices, or inject another TMT step. The compatibility spelling
`provision-extra-args` is accepted, but must not be combined with
`provision.extra-args`.

An explicit `tmt.plan` or `--plan` selects the exact absolute plan name, not a
regular expression. VM execution requires one explicit plan and currently
supports one provision phase. A declared VM user is preserved; otherwise the
VM test user is `azureuser`. Non-VM TMT runs may retain native enabled-plan
selection when no plan is specified.

The existing ad-hoc component TMT workflow remains available: when there are no
associated tests and no explicit `--test`, `--source-dir` can supply the fmf
tree, with `--plan` also required for VM execution. It still requires a named
component; this is not projectless execution or automatic framework detection.

## 8. Results, ownership, and automation

### 8.1 Outcome contract

| Outcome | Meaning | Normally completed CLI exit |
|---------|---------|----------------------------|
| `passed` | The selected invocation passed | 0 |
| `skipped` | All collected cases were skipped | 0 |
| `failed` | Test assertions failed | 1 |
| `error` | Preparation, provisioning, execution, reporting, or cleanup failed | 1 |
| `cancelled` | Cancellation was handled and reported | 1 |
| `planned` | A successful dry run; no tests executed | 0 |

Collecting no native cases is an error, not a skip or success. Selecting no
configured tests for execution is also an error. An infrastructure failure takes
precedence over otherwise passing or failing assertions.

After a failure, retained results must distinguish tests that ran from those
not started because of `--fail-fast`. A successful exit does not guarantee that
any case passed: CI requiring a non-skipped case must enforce that policy.

### 8.2 Retained evidence

Non-dry-run invocations normally retain their own directory under
`<work-dir>/tests/run-<id>/`.
The customer can find:

- `results.json`: normalized outcomes per named test, with native cases and
  their original statuses when available.
- Per-test `inputs.json`: selected artifact provenance, effective test settings,
  and the selected TMT bootstrap origin when applicable.
- Native logs/reports and runtime version information.
- An optional aggregate JUnit file at the explicitly requested `--junit-xml` path.

Normalized JSON uses run fields such as `name`, `framework`, `strategy`,
`status`, `duration`, `outputDir`, optional `error`, and `cases`. Cases retain
their name, normalized/native status, duration, and available diagnostics.
JSON durations are nanoseconds. Additional orchestration failures may appear
without native cases.

Input evidence distinguishes original file paths from private run inputs and
records consumed digests. Snapshot paths need not exist after cleanup; customers
must retain original artifacts separately for reproduction. Bootstrap provenance
identifies the winning distro-image, project-image, or per-test configuration.

Early configuration errors, forced termination, or unwritable output storage can
prevent reports from being created. Missing reports are not evidence of success.
The exit status remains authoritative even if recorded cases passed.

`test list -O json` provides a structured catalog listing. For execution,
`-O json` formats the final result but does not reserve stdout exclusively for
JSON; native framework output may share it. A dedicated `--results-json` option
is deferred. CI should use a unique work root and consume the retained file.

### 8.3 Ownership and prerequisites

Testing must not modify original input artifacts or managed local test-source
trees. An active run must continue consuming its selected input bytes if another
process rebuilds the original artifact. Managed framework environments and guests
are temporary; retain diagnostics before normal cleanup.

The existing pytest prepared environment remains caller-owned. Absolute pytest
selectors outside the declared source directory also remain caller-owned paths;
they are not covered by the private-source-copy guarantee.

Host/SSH execution may modify the designated target. Graceful cancellation must
request cleanup of managed resources without destroying a caller-owned SSH
machine. Forced termination can prevent cleanup and report retention.

Tests, plugins, and guest-bootstrap commands must be trusted. Private workspaces
and disposable guests are not a sandbox against malicious framework code running
as the invoking user. Logs may contain sensitive data and need appropriate
access/retention policies.

Customers must provision the appropriate host virtualization/container tools and
guest prerequisites. In particular, images must support the selected provisioning
environment; an image that boots in Azure is not automatically cloud-init-compatible
with QEMU. Any compatibility customization must produce a separately identified
artifact, not silently alter the original or suppress provisioning failures.

## 9. Compatibility and behavior changes

The existing `azldev component test NAME` and `azldev image test NAME` entry points
remain supported. New documentation should lead with `azldev test run`.

| Existing spelling | Proposed preferred spelling |
|-------------------|-----------------------------|
| `component test NAME` | `test run --component NAME` |
| `image test NAME` | `test run --image NAME` |
| Image `--test-suite NAME` | `--test NAME` |
| Component `--image-path FILE` | `--base-image-path FILE` |
| Component `--provision virtual` | `--execution vm` |
| Component `--provision local` | `--execution host` |
| Deprecated component `--from-spec` | `--source-dir` pointing to the prepared/rendered fmf tree |

Legacy component-only aliases stay on the legacy component command; they do not
change the meaning of `test run --image-path`.

Keeping the entry points does **not** mean every existing invocation has
unchanged behavior. Stakeholders must review these changes:

| Area | Customer-visible change |
|------|-------------------------|
| Missing local artifacts | Test execution may now build missing subject artifacts. Supply explicit inputs when that is not desired. |
| Image execution context | Image testing requires a defined project default distro/version even for an explicit image file; a local build recipe is not required. |
| Component inputs | Explicit `--rpm` is no longer mandatory; the default is the component's local binary build output set. |
| Older build output | Output without sufficient component/architecture identity may require explicit paths or a new build rather than automatic reuse. |
| Component test selection | Explicit selectors can select project-wide test/group names, not just filter associated TMT names. Unsupported combinations and unknown selectors are errors. |
| Failure continuation | Component execution no longer stops after the first named test by default; request `--fail-fast` for that behavior. |
| Empty image test selection | Previously a warning with success; now an execution error. Empty `test list` remains successful. |
| Paths/output | Component test output defaults to the project's work directory rather than the caller's current directory. Relative image-test/JUnit paths consistently use the selected project. |
| Runtime/source ownership | Managed runtimes and source copies are private to a run; scripts must not depend on old reusable runtime directories. The established pytest `install=none` location remains supported. |
| Guest bootstrap | Implicit account/firewall accommodations are removed. Declare needed commands on a named image or test. |
| Plan selection | Explicit TMT plans are literal names; callers must not rely on incidental regular-expression matching. |
| Packaging locks | Test subject lookup skips packaging-lock consistency checks, including the initial lookup when execution falls back to a build. Normal `component build` validation is unchanged. |

The lock-validation behavior is a deliberate scope choice, not promotion of
`--without-lockfile`. Customers requiring the normal consistency check should
run `component build` before testing.

No removal date is proposed for the retained entry points or aliases. Existing
test/group associations, LISA criteria, pytest installation modes, image
properties, and external scheduling metadata remain supported.

## 10. Acceptance criteria

Acceptance is expressed in observable customer behavior, not internal code
structure.

| ID | Requirement | Observable acceptance |
|----|-------------|-----------------------|
| A1 | U1, U6 | The shared command lists catalog entries or subject associations and expands groups without running frameworks or acquiring inputs. An empty list succeeds; unknown selectors fail. |
| A2 | U1, U5 | Supported LISA, TMT, and pytest tests run for their supported subject/strategy combinations. Unsupported selections fail rather than being silently omitted. |
| A3 | U2, U3 | Default subject reuse, missing-output builds, and forced rebuilds are distinguishable. Supporting-image origin can change without changing subject origin. |
| A4 | U3, U4 | A named published image can be consumed without a local recipe. Missing catalog variants fail explicitly; no silent local/LKG fallback occurs. |
| A5 | U4 | Explicit RPM/image files can be tested in a small configured project without dummy packaging sources/locks or experimental mode. |
| A6 | U5 | The same logical image resolves the same bootstrap policy across local, published, and explicitly associated file inputs. Project/test overrides replace lists; `[]` clears; anonymous base files do not inherit a guessed identity. |
| A7 | U7 | Passing, assertion-failing, infrastructure-error, all-skipped, zero-case, and cancelled runs produce the documented classifications and process outcomes when reporting is possible. |
| A8 | U7 | Multiple selected tests retain their individual evidence and contribute to aggregate JUnit. Fail-fast distinguishes unstarted tests from passing ones; missing reports cannot produce a false successful CI interpretation. |
| A9 | U8 | A concurrent rebuild does not replace the bytes an active test consumes. Original artifacts and managed source trees remain unchanged; reports identify the consumed inputs. |
| A10 | U8 | Managed resources receive normal/graceful cleanup, while caller-owned SSH machines and prepared pytest environments remain caller-owned. |
| A11 | U6, U7 | Listing JSON is independently consumable. Execution automation has a documented retained-file workflow that does not assume stdout contains only JSON. |
| A12 | U1-U8 | The retained commands/aliases, changed defaults, prerequisite boundaries, and complete component/image/CI walkthroughs are documented together. |

## 11. Scope limits and deferred requirements

The initial scope does not include:

- Dynamic enumeration of native cases, including discovery that conditionally
  fetches sources, installs runtimes, acquires artifacts, or provisions guests.
- A dedicated `--results-json` destination or distinct process exit codes for
  every result category.
- Projectless testing; subjects still need a project/catalog identity.
- Automatic conflict-aware RPM subset selection or a candidate-repository
  package-solving workflow.
- Remote cloud provisioning, Azure SKU fan-out, or a new local capability-based
  scheduling policy. Existing scheduling metadata remains available to external
  orchestration.
- A generic environment-profile system or cross-framework bootstrap language.
  The new image defaults are specifically for managed TMT VMs.
- Multi-guest TMT VM plans, arbitrary new frameworks, or a promise that every
  framework supports every execution strategy.
- Automatic promotion of artifacts to LKG or proof that reused local output
  matches the current source tree.

The architecture flag accepts `x86_64` and `aarch64`, but this proposal does not
claim a complete native qualification matrix for every format/architecture pair.
VM availability, framework setup, and network access also preclude a universal
execution-time guarantee. The firm low-cost boundary is configuration-only listing.

## 12. Stakeholder decisions requested

The proposal asks stakeholders to approve the customer contract above, particularly:

1. **Defaults:** local reuse-or-build for the subject, published LKG for a
   component's supporting image, with no freshness claim or silent fallback.
2. **Identity:** one image name within a distro/version, with explicit project
   overrides affecting both local and published runs.
3. **Safety and migration:** the execution/ownership boundaries and behavior
   changes in section 9, including lock validation and explicit guest bootstrap.
4. **First-release boundaries:** cheap catalog listing now, with dynamic
   discovery and a dedicated JSON report destination deferred.

Reviewers should distinguish disagreements with these product choices from
requests to expand the deferred scope. Neither requires discussion of internal
package layout or implementation technique.

## Related customer documentation

- [Artifact testing workflows](../user/how-to/test-build-artifacts.md)
- [Complete component testing example](../user/how-to/test-component.md)
- [Complete image testing examples](../user/how-to/test-image.md)
- [CI and result consumption](../user/how-to/automate-artifact-tests.md)
- [Tests and test groups](../user/reference/config/tests.md)
- [Published artifacts](../user/reference/config/distros.md#published-test-artifacts)
- [Named image test defaults](../user/reference/config/images.md#image-test-defaults)
