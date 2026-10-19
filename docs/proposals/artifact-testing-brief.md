# Stakeholder brief: Consistent artifact testing

**Draft for review.** A short companion to the [full PRD](artifact-testing-prd.md).

## The customer need

Package and image maintainers need one predictable way to test their artifacts,
regardless of the test framework or where the artifacts were built.

Upstream currently splits that experience: component testing runs TMT, while
image testing runs LISA and pytest but rejects TMT. Package testing requires
explicit RPMs and, for VM runs, an explicit image. Customers also lack shared controls for
local versus published inputs, inexpensive test listing, and consistent CI
results.

## Proposed experience

Introduce a shared `azldev test` family:

```sh
azldev test list --component curl
azldev test run --component curl --rebuild
azldev test run --image vm-base --artifact-source lkg
azldev test run --image vm-base --image-path ./candidate.qcow2 --junit-xml results.xml
```

`list` shows the existing named test catalog, optionally filtered by component
or image. It does not download inputs, prepare frameworks, or discover individual
native cases.

`run` selects one component or image. It runs associated tests by default;
`--test NAME_OR_GROUP` selects explicit tests/groups instead. The existing TOML
test catalog and native test languages remain in use.

Both artifact kinds gain access to TMT, LISA, and pytest within their supported
environments. LISA uses VMs; pytest inspects artifacts on the host, without
booting them. TMT supports VM/container image tests and additionally SSH/host
component tests. Unsupported combinations are errors, not silently skipped work.

## Predictable artifact choices

The artifact **under test** and its **supporting image** are independent:

| Input | Default | Customer controls |
|-------|---------|-------------------|
| Subject packages or image | Reuse local output; build if missing | `--rebuild`, `--artifact-source lkg`, or explicit `--rpm` / `--image-path` |
| Supporting image for component tests | Named published LKG image | `--base-image`, `--base-image-source local`, `--rebuild-base-image`, or `--base-image-path` |

For example, rebuilding `curl` does not rebuild its base OS. Published images
can be consumed without a local build recipe. Explicit files bypass acquisition
of those artifacts, but framework setup may still need networking.

LKG means publisher-advertised last-known-good, not an azldev certification.
Reused output is not claimed to match current sources. Failed builds/downloads
must not silently switch artifact origins.

## Configuration and image identity

Keep `[tests]`, `[test-groups]`, and their existing associations. Add:

- Test execution defaults: strategy and named supporting images.
- Distro-version catalogs: published `images`, complete `test-packages` sets,
  and `test-base-images` defaults.
- Named-image TMT bootstrap: `test-defaults.tmt.cloud-init-runcmds`.
- Pinned pytest sources and additional explicit TMT configuration.

**The same image name within a distro/version means one logical image**, whether
its bytes are local or published. TMT guest setup comes from the distro image,
then explicit project-image overrides, then per-test overrides. Lists replace;
`[]` disables inherited commands. Project overrides therefore affect LKG runs too.

A bare `--base-image-path` is anonymous and inherits no guessed image identity.
Adding `--base-image vm-base` explicitly associates that file with the named
image. Bootstrap settings apply only to managed TMT VMs, not the host or other
frameworks.

## Guardrails and migration

The existing `component test` and `image test` commands remain supported, but
behavior changes need review: missing output may now trigger builds, an empty
execution selection is an error, and tests continue after failures unless
`--fail-fast` is requested.

Execution still needs a configured project and default distro/version, even for
supplied files. It does not require `--without-lockfile`. Test selection skips
packaging-lock consistency checks, including before implicit builds; customers
needing that check should run `component build` first.

Original artifacts and managed test-source trees are preserved. Host/SSH tests
can modify their designated targets; disposable environments and trusted tests
remain essential.

CI receives consistent outcomes, retained native evidence, and optional aggregate
JUnit. Failures return nonzero; all-skipped runs succeed, while zero collected
cases are errors. Execution stdout may contain native logs even with `-O json`,
so automation should read the retained `results.json` in a unique work directory.

## Review decisions and scope

Approve the shared CLI, local-subject/LKG-base defaults, shared-name image policy,
and documented migration effects. Success means predictable input selection and
usable results across the three frameworks.

**Deferred:** dynamic native-case discovery, `--results-json`, projectless
execution, automatic conflict-aware RPM selection, and cloud/SKU orchestration.
The [full PRD](artifact-testing-prd.md) contains detailed syntax, schema examples,
compatibility changes, and acceptance criteria.
