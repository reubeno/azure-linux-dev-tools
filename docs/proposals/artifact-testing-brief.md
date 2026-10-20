# Stakeholder brief: Consistent artifact testing

**Draft for review.** A short companion to the [full PRD](artifact-testing-prd.md).

## The customer need

Package and image maintainers need one predictable way to test their artifacts,
regardless of the test framework or where the artifacts were built.

Upstream currently splits that experience: component testing runs TMT, while
image testing runs LISA and pytest but rejects TMT. Artifact selection and CI
results also lack consistent controls.

## Proposed experience

Make the existing `component test` and `image test` verbs consistent:

```sh
azldev component test curl --rebuild
azldev image test vm-base --artifact-source lkg
azldev image test vm-base --image-path ./candidate.qcow2 --junit-xml results.xml
```

## Separable test choices

Choose each dimension independently, within supported framework combinations:

| Choice | Default | Customer controls |
|--------|---------|-------------------|
| **Subject:** what is being evaluated | Explicitly named | `component test curl` tests RPMs; `image test vm-base` tests an image |
| **Tests:** which checks to run | The subject's associated tests/groups | `--test NAME_OR_GROUP` replaces that selection |
| **Framework:** how checks are expressed | Declared by each named test | `[tests.NAME].type = "tmt"`, `"lisa"`, or `"pytest"` |
| **Execution:** where checks run | The test's `execution.strategy`, otherwise VM for TMT/LISA and host for pytest | `--execution vm`, `container`, `ssh`, or `host`, where supported |
| **Architecture:** which target variant | Host architecture | `--arch x86_64` or `--arch aarch64` |
| **Test source:** where test code comes from | Configured pinned repository or local pytest directory | `--source-dir` for TMT/pytest; `--lisa-dir` for a local LISA checkout |
| **Image identity:** which logical image | Image subject name; for component bases, test/distro configuration | `--base-image NAME` selects a supporting image independently of its artifact origin |

For example, `--source-dir` tries edited tests against the same selected artifacts.
`--execution` changes the target environment, not the framework. Artifact-origin
flags change candidate bytes, not test selection.

LISA uses VMs; pytest inspects artifacts on the host without booting them.
TMT supports VM/container image tests and additionally SSH/host component tests.
Unsupported combinations are errors, not silently skipped work.

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

## Configuration connects the choices

Keep the existing test catalog and associations; add published inputs, execution
defaults, and image-specific setup:

**Publishers advertise LKG locations and image-specific test setup** in the selected
distro/version. These snippets extend an existing project; URLs and commit SHA
are illustrative placeholders.

```toml
[distros.azurelinux.versions."4.0"]
release-ver = "4.0"
test-base-images = { vm = "vm-base" }

[distros.azurelinux.versions."4.0".images.vm-base.architectures]
x86_64 = [{ format = "qcow2", uri = "https://example.test/lkg/vm-base.qcow2" }]

[distros.azurelinux.versions."4.0".images.vm-base.test-defaults.tmt]
cloud-init-runcmds = [
  "firewall-cmd --add-service=ssh && firewall-cmd --add-port=10022/tcp",
]
```

The firewall command runs **inside a managed TMT guest before readiness checks**.
It is appropriate for a firewalld image using testcloud's session connection,
not a universal requirement or a host firewall change. Other architectures and
formats have their own catalog entries; downloads may include `sha256`.
Published RPM sets similarly live under the distro version's `test-packages`.

**Test authors select the framework, sources, and execution defaults**, then
associate that test with a subject:

```toml
[tests.curl-smoke]
type = "tmt"

[tests.curl-smoke.execution]
strategy = "vm"
base-images = { vm = "vm-base" }

[tests.curl-smoke.tmt]
source = { git-url = "https://example.test/curl-tests.git", ref = "0123456789abcdef0123456789abcdef01234567" }
plan = "/plans/smoke"

[components.curl.tests]
tests = [{ name = "curl-smoke" }]
```

`azldev component test curl` now combines local candidate RPMs, the published
`vm-base`, and the pinned TMT plan. The per-test `base-images` mapping overrides
the distro default; `--base-image` overrides both. Image testing uses its subject
image instead of a separate base.

**Image identity remains independent of origin.** Within a distro/version,
`vm-base` means the same logical image whether local or published. Guest setup
resolves from the catalog, then `[images.vm-base.test-defaults.tmt]`, then the
selected test's `tmt` settings. Explicit lists replace earlier lists; `[]` clears
them. Project overrides therefore apply to LKG runs too.

A bare `--base-image-path` is anonymous. Add `--base-image vm-base` to explicitly
associate that file with the image's defaults. These bootstrap defaults apply
only to managed TMT VMs, not other frameworks or execution strategies.
