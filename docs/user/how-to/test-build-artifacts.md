# Test build artifacts

Use `azldev component test NAME` for a component's binary packages, and
`azldev image test NAME` for an image. Both commands select configured tests
from the same `[tests]` / `[test-groups]` catalog and use the same LISA, TMT,
and pytest adapters.

## Select the subject and its supporting image

The default subject is a local build: existing output is reused, or the normal
build pipeline runs when output is missing. `--rebuild` forces a new local
build. Build records retain exact output membership and timestamps; the log
reports artifact age, not a claim that the artifact matches current sources.
Legacy files without build timestamps are identified by modification age.

```sh
azldev component test curl
azldev component test curl --rebuild
azldev image test vm-base
azldev image test vm-base --rebuild
```

Supporting VM/container images default to published LKG artifacts declared in
the selected distro version. Configure the [published artifact
catalog](../reference/config/distros.md#published-test-artifacts) first; there
are no hard-coded cloud storage endpoints.

Subject and supporting-image overrides are independent:

```sh
# Local subject, explicitly chosen supporting image
azldev component test curl --base-image vm-base

# Rebuild the supporting image using this project's recipe
azldev component test curl --base-image vm-base --rebuild-base-image

# Explicit on-disk inputs; no implicit package/image build
azldev component test curl --rpm './out/curl*.rpm' --base-image-path ./base.qcow2
azldev image test vm-base --image-path ./base.qcow2

# Explicitly test published subjects instead of local changes
azldev component test curl --artifact-source lkg
azldev image test vm-base --artifact-source lkg
```

LKG base-image downloads work without a local recipe. Rebuilding a base image
requires one. A failed build does not fall back to LKG.

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
azldev component test curl --execution container
azldev component test curl --execution ssh --ssh-host disposable-vm --ssh-user tester --ssh-key ./id_test
azldev component test curl --execution host
```

Host execution can install packages and modify the machine. TMT host execution
requires Azure Linux 4 and sudo. SSH execution modifies an existing target;
the tool does not destroy that caller-owned machine. Use designated disposable
test systems. VM/container provisioning is delegated to the framework.

TMT VM execution currently requires an explicit plan (`tmt.plan` or `--plan`)
so the plan's hardware requirements can be exported and preserved. Containers
consume an OCI registry image or a local OCI archive. Unsupported framework
and execution combinations fail explicitly before framework setup.

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
azldev component test curl --source-dir ./prepared/curl --plan /plans/all
azldev image test vm-base --lisa-dir ./lisa --test boot-smoke
```

Keep source and work directories separate. Configured Git sources are pinned
to full commits. Pytest may additionally declare a pinned `pytest.source` and
a checkout-relative `working-dir`.

## Select tests and inspect results

```sh
azldev component test curl --test smoke --test regression --junit-xml ./results.xml
azldev image test vm-base --test boot-smoke --fail-fast
```

Selectors accept either test names or test-group names. Duplicates are run once;
unknown selectors are errors even when another selector matches. Without
selectors, associated tests run. An empty selection is an error.

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
an error, not a successful or skipped run. Reporting or cleanup errors remain infrastructure errors even when
the native tests also failed; an aggregate report never hides a failed run
behind otherwise passing native cases.

TMT results include native per-case outcomes and log paths; aggregated JUnit is
derived from `execute/results.yaml`, not a synthetic single-case success.
The plan's native reporting plugins remain intact. `tmt-output.log` retains
combined output, and `python-environment.txt` records the installed TMT runtime.
All three adapters populate normalized per-case statuses in `results.json`,
retaining `nativeStatus`, case names, durations, and available diagnostic messages.
Native JUnit remains available for LISA and pytest. Missing or contradictory
report data is an infrastructure error even when the framework exits successfully.
Cancellation gives TMT time to unwind; a bounded, run-specific cleanup handles
managed guests before the private environment is removed.
The adapter requires a fresh native result directory for each run, so an early
failure cannot accidentally report cases from an older run. Both CLI commands
allocate this directory automatically.

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

Relative CLI paths resolve against the selected project directory (`-C`).
`--work-dir` chooses the root for run output. The default is the project's work
directory, not the caller's current directory.

The old `--test-suite` flag is an alias for `--test`. Component `--image-path`
is an alias for `--base-image-path`, and `--provision virtual/local` maps to
`--execution vm/host`. Existing TOML framework definitions remain supported.
