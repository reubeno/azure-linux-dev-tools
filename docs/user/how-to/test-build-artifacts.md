# Test build artifacts

Use `azldev test run --component NAME` for a component's binary packages, and
`azldev test run --image NAME` for an image. Both forms select configured tests
from the same `[tests]` / `[test-groups]` catalog and use the same LISA, TMT,
and pytest adapters.

For a first run with complete configuration and test files, start with
[Test a component](test-component.md) or [Test an image](test-image.md).
For CI, see [Automate artifact tests](automate-artifact-tests.md).
The examples on this page assume an existing project with named subjects and
associated tests; paths alone do not replace that configuration.

## List configured tests

```sh
azldev test list
azldev test list --component curl
azldev test list --image vm-base
azldev test list --test regression -O json
```

Without a subject, `list` shows the whole named test catalog. With a subject, it
expands that subject's test/group associations and removes duplicates. Explicit
`--test` selectors replace those associations, matching `run`. An empty list is
successful; an unknown subject or selector is an error.

Listing reads configuration only. It does not clone test sources, install
frameworks, acquire artifacts, or execute framework code. Each row is a named
configured invocation, not an individual framework case. Dynamic discovery is
not implemented. Component test selection and listing do not require packaging
lock consistency; they do not opt into `--without-lockfile`.
The test command also skips that initial consistency check when it falls back
to building missing packages. `azldev component build` retains its normal
validation; run it first when you need that check before testing.

## Select the subject and its supporting image

The default subject is a local build: existing output is reused, or the normal
build pipeline runs when output is missing. `--rebuild` forces a new local
build. Build records retain exact output membership and timestamps; the log
reports artifact age, not a claim that the artifact matches current sources.
Legacy files without build timestamps are identified by modification age.

Before execution, selected files are copied into private, read-only snapshots
and hashed. A concurrent rebuild cannot replace an active run's input; changes
detected while copying are errors rather than a mixed-version test. Snapshots
are not hardlinks and are removed after execution. Manifests retain `originalPath`
and `sha256` so the consumed bytes can be identified afterwards. This protects
normal build/test concurrency and accidental writes, not hostile code running
with the same user privileges.

```sh
azldev test run --component curl
azldev test run --component curl --rebuild
azldev test run --image vm-base
azldev test run --image vm-base --rebuild
```

Supporting VM/container images default to published LKG artifacts declared in
the selected distro version. Configure the [published artifact
catalog](../reference/config/distros.md#published-test-artifacts) first; there
are no hard-coded cloud storage endpoints.

Subject and supporting-image overrides are independent:

```sh
# Local subject, explicitly chosen supporting image
azldev test run --component curl --base-image vm-base

# Reuse a local supporting image, building it if missing
azldev test run --component curl --base-image vm-base --base-image-source local

# Rebuild the supporting image using this project's recipe
azldev test run --component curl --base-image vm-base --rebuild-base-image

# Explicit on-disk inputs; no implicit package/image build
azldev test run --component curl --rpm './out/curl*.rpm' --base-image-path ./base.qcow2
azldev test run --image vm-base --image-path ./base.qcow2

# Explicitly test published subjects instead of local changes
azldev test run --component curl --artifact-source lkg
azldev test run --image vm-base --artifact-source lkg
```

LKG base-image downloads work without a local recipe. Rebuilding a base image
requires one. `--base-image-source local` uses azldev's recorded local output
(or recognized legacy output), not an arbitrary file in the current directory;
if no local output exists, it builds using the project-local recipe.
Use `--base-image-path` for a particular file. A failed build does not fall back
to LKG.

Live component tests install all selected binary subpackages. **TODO:** automatic
conflict-aware selection is deferred; if output subpackages conflict, use
explicit `--rpm` paths to select a compatible subset. Pytest receives package
files through the input manifest rather than installing RPMs on the host.

## Select an execution strategy

| Framework | Component execution | Image execution |
|---|---|---|
| TMT | VM, container, SSH, host | VM, container |
| LISA | VM | VM |
| pytest | Host-side artifact inspection | Host-side artifact inspection |

```sh
azldev test run --component curl --execution container
azldev test run --component curl --execution ssh --ssh-host disposable-vm --ssh-user tester --ssh-key ./id_test
azldev test run --component curl --execution host
```

Host execution can install packages and modify the machine. TMT host execution
requires Azure Linux 4 and sudo. SSH execution modifies an existing target;
the tool does not destroy that caller-owned machine. Use designated disposable
test systems. VM/container provisioning is delegated to the framework.

TMT VM execution currently requires an explicit plan (`tmt.plan` or `--plan`)
so the plan's hardware requirements can be exported and preserved. Containers
consume an OCI registry image or a local OCI archive. Unsupported framework
and execution combinations fail explicitly before framework setup.

For VM images that need first-boot accommodations, configure
[named image defaults](../reference/config/images.md#image-test-defaults) or the
named test's [`tmt.cloud-init-runcmds`](../reference/config/tests.md#tmt-fields).
There are no implicit azldev firewall or `fedora` account workarounds. In
particular, testcloud's session-mode HTTP readiness listener needs TCP 10022
reachable before provisioning can finish.

Within the selected distro/version, a shared image name identifies one logical
image. TMT bootstrap settings resolve from its distro catalog entry, then any
explicit project-image override, then the per-test override. Switching between
local and LKG artifacts does not switch policy. Lists replace rather than append;
`[]` explicitly disables inherited commands.

A component's `--base-image-path` alone is anonymous: it does not inherit the name
or defaults of the test/distro's usual base image. Add `--base-image vm-base` to
explicitly associate that file with the named image. An image subject is already
explicitly named by `--image NAME`. The selected bootstrap origin is logged and
recorded as `tmtBootstrapSource` in the input manifest.

## Override test sources

`--source-dir` runs TMT or pytest from a local test tree; `--lisa-dir` selects a
local LISA framework checkout. These are source overrides, not artifact-source
overrides. LISA's configured source identifies the framework, while TMT's
source identifies the plan repository. TMT's own discovery step still controls
where its test cases come from.

When selecting a mixed-framework group, framework-specific overrides apply
only to the corresponding tests. An override with no applicable selected test
is an error rather than being silently ignored.

```sh
azldev test run --component curl --source-dir ./prepared/curl --plan /plans/all
azldev test run --image vm-base --lisa-dir ./lisa --test boot-smoke
```

Keep source and work directories separate. Configured Git sources are pinned
to full commits. Pytest may additionally declare a pinned `pytest.source` and
a checkout-relative `working-dir`.

## Select tests and inspect results

```sh
azldev test run --component curl --test smoke --test regression --junit-xml ./results.xml
azldev test run --image vm-base --test boot-smoke --fail-fast
```

Selectors accept either test names or test-group names. Duplicates are run once;
unknown selectors are errors even when another selector matches. Explicit
selectors replace the associations for this invocation. Without selectors,
associated tests run. An empty selection is an error.

Each invocation retains an isolated result directory beneath `<work-dir>/tests/`.
TMT executes from a short, owned `/var/tmp` workspace, so SSH guests do not need
permission to recreate the developer's home-directory path and SSH control
sockets stay short. Native state is collected into the result directory before
the temporary workspace is removed.
Per-test input manifests record paths and provenance; native reports remain in
per-test directories, and `results.json` records normalized outcomes. An
optional aggregate JUnit report includes native cases when available and
explicit setup/teardown failures. Assertions, execution errors, skips, and
cancellation remain distinguishable.
All frameworks report an all-skipped run as `skipped`; collecting no cases is
an error, not a successful or skipped run. Reporting or cleanup errors remain
infrastructure errors even when the native tests also failed; an aggregate
report never hides a failed run behind otherwise passing native cases.

TMT results include native per-case outcomes and log paths; aggregated JUnit is
derived from `execute/results.yaml`, not a synthetic single-case success.
The plan's native reporting plugins remain intact. `tmt-output.log` retains
combined output. Every adapter records its installed runtime distributions in
`python-environment.txt` before removing the private environment.
Source-installed distributions also record their direct-source metadata, including
the resolved Git commit. `tmt-requirements.txt` retains TMT's exact input constraints.
All three adapters populate normalized per-case statuses in `results.json`,
retaining `nativeStatus`, case names, durations, and available diagnostic messages.
Native JUnit remains available for LISA and pytest. Missing or contradictory
report data is an infrastructure error even when the framework exits successfully.
LISA's explicit `deployment failed.` phase marker is normalized to an infrastructure
error, even though its native JUnit attaches that failure to a selected test case.
Cancellation gives TMT time to unwind; a bounded, run-specific cleanup handles
managed guests before the private environment is removed.
The adapter requires a fresh native result directory for each run, so an early
failure cannot accidentally report cases from an older run. Both CLI commands
allocate this directory automatically.

For TMT VMs, the adapter adjusts only `ssh_pwauth` in the fully rendered
cloud-init configuration before the seed image is built; it does not copy or
rewrite TMT's template. Non-root privileged artifact collection uses remote
`sudo -n rsync`, leaving guest ownership and permissions unchanged. This
adaptation applies only to managed testcloud guests, not caller-owned SSH targets.

Writable framework checkouts and installed environments are private to a run.
Caller sources are copied, including uncommitted changes, rather than modified
by tests or editable Python installation. Only verified, content-addressed
download bytes are reused across runs; changing LKG URLs cannot replace a
previous run's selected input. Existing `pytest install=none` environments
remain explicitly caller-managed.

Pytest tests can read the JSON file at `AZLDEV_TEST_INPUTS`, or receive its path
through `{inputs-path}` in `extra-args`. Existing image placeholders continue
to work. The manifest has `subject`, `kind`, and `inputs` fields; `inputs.image`
describes an image when applicable and `inputs.packages` lists candidate RPMs.
File artifacts have `path`; container-store images have `ociImageId` instead.
Those locations are mutually exclusive. Pytest tests using an OCI identity
must read it from the manifest; `{image-path}` is only valid for file artifacts.

Relative CLI paths resolve against the selected project directory (`-C`).
`--work-dir` chooses the root for run output. The default is the project's work
directory, not the caller's current directory.

Exit code 0 includes all-skipped runs; failures and infrastructure errors return
nonzero. Early configuration errors may produce no results file, and native
output can share stdout with `-O json`. Use a unique work root and consume the
retained JSON as shown in [Automate artifact tests](automate-artifact-tests.md).

The older `azldev component test NAME` and `azldev image test NAME` commands remain
supported. Only the legacy component command accepts `--image-path` as an alias
for `--base-image-path`, and `--provision virtual/local` for `--execution vm/host`.
For `test run`, `--image-path` always means the subject image and requires
`--image`; supporting images use `--base-image-path`. The old `--test-suite`
flag remains an alias for `--test`. Existing TOML framework definitions remain supported.
