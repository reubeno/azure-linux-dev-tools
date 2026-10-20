# Stakeholder brief: Consistent artifact testing

**Draft for review.**

## The customer need

Maintainers need predictable artifact testing across frameworks and build origins.
Today azldev splits that experience: component testing runs TMT; image testing runs
LISA and pytest but rejects TMT. Artifact selection and CI results also lack
consistent controls.

## Everyday workflows

Make `component test` and `image test` consistent. Developers normally rely on
project defaults. These examples assume configured subjects and associated tests.

**"I changed curl; test the new build against our standard base OS."**

```sh
azldev component test curl --rebuild
```

Build fresh RPMs; the supporting image still comes from the configured LKG.
Rebuilding curl does not rebuild its base.

**"CI gave me an image; run our checks and produce a report."**

```sh
azldev image test vm-base --image-path ./candidate.qcow2 --junit-xml results.xml
```

Test that file without building the image. CI gets aggregate JUnit and retained
evidence distinguishing assertion failures, infrastructure errors, and skips.

**"Compare with the published baseline using the same tests."**

```sh
azldev image test vm-base --artifact-source lkg --junit-xml lkg-results.xml
```

Select publisher-advertised bytes while keeping the project's tests and policy.

## Separate controls when needed

Test authors and distro maintainers establish defaults; developers override only
the choice relevant to their task:

| Choice | Default | Customer controls |
|--------|---------|-------------------|
| **Subject:** what is evaluated | Explicitly named | `component test NAME` or `image test NAME` |
| **Tests:** which checks to run | The subject's associated tests/groups | `--test NAME_OR_GROUP` replaces that selection |
| **Framework:** how checks are expressed | Declared by each named test | `[tests.NAME].type = "tmt"`, `"lisa"`, or `"pytest"` |
| **Execution:** where checks run | `execution.strategy`; otherwise VM for TMT/LISA, host for pytest | `--execution vm`, `container`, `ssh`, or `host`, where supported |
| **Architecture:** which target variant | Host architecture | `--arch x86_64` or `--arch aarch64` |
| **Test source:** where test code comes from | Pinned repository or local pytest directory | `--source-dir` for TMT/pytest; `--lisa-dir` for LISA |
| **Image identity:** which logical image | Subject name, or configured component base | `--base-image NAME`, independently of artifact origin |

For example, `--source-dir ./tests/curl` tries edited tests without changing the
artifact-origin policy.

Consistency means common selection and reporting, not identical capabilities.
LISA uses VMs; pytest inspects artifacts on the host without booting or installing
candidate RPMs. TMT supports VM/container image tests and additionally SSH/host
component tests. Unsupported combinations are errors.

## Predictable artifact choices

The artifact **under test** and its **supporting image** are independent:

| Input | Default | Customer controls |
|-------|---------|-------------------|
| Subject packages or image | Reuse local output; build if missing | `--rebuild`, `--artifact-source lkg`, or explicit `--rpm` / `--image-path` |
| Supporting image for component tests | Named published LKG image | `--base-image`, `--base-image-source local`, `--rebuild-base-image`, or `--base-image-path` |

**Defaults favor quick reruns, not automatic freshness detection.** Plain
`component test curl` can reuse yesterday's RPMs; use `--rebuild` after source
changes. Build-if-missing avoids a separate first-build step but can be expensive.
Explicit files avoid acquiring those inputs; framework setup may still need networking.

Published images need no local recipe. Failed acquisition never silently switches
origins. LKG is the publisher's approval, not an azldev certification.

## Configuration connects the choices

Keep the existing test catalog and associations; add published inputs, execution
defaults, and image-specific setup:

**Publishers choose approved artifacts; distro maintainers version the catalog
and test setup.** azldev reads project configuration, not an approval service.
These snippets extend an existing project; URLs and commit SHA are placeholders.

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

The firewall command is **maintainer-owned compatibility metadata**, not copied
into every package test. It opens SSH and testcloud's session-mode readiness port
in the disposable guest, keeping that test-only port out of the shipping image.
It is specific to this firewalld image, not a universal default.

Mutable LKG URLs can change between runs. For repeatability, pin artifact
locations/checksums and configuration revisions; runs record consumed digests.
Other image variants and published RPM sets also belong in the catalog.

**Test authors define checks and associate them with subjects:**

```toml
[tests.curl-smoke]
type = "tmt"

[tests.curl-smoke.tmt]
source = { git-url = "https://example.test/curl-tests.git", ref = "0123456789abcdef0123456789abcdef01234567" }
plan = "/plans/smoke"

[components.curl.tests]
tests = [{ name = "curl-smoke" }]
```

This test inherits TMT's VM strategy and the distro's `vm-base`; neither needs
repeating. Only exceptions need `[tests.NAME.execution]` settings for `strategy`
or `base-images`. CLI overrides take precedence. Image tests use their subject
image, not a separate base.

**Image identity remains independent of origin.** Within a distro/version,
`vm-base` means the same logical image whether local or published. Guest setup
resolves from the catalog, then `[images.vm-base.test-defaults.tmt]`, then the
selected test's `tmt` settings. Explicit lists replace earlier lists; `[]` clears
them. Project overrides deliberately apply to LKG runs too: the comparison is
between artifacts under **the project's test policy**, not identical environments.
The effective bootstrap policy and its origin are retained with the run.

A bare `--base-image-path` is anonymous. Add `--base-image vm-base` to explicitly
associate that file with the image's defaults. These bootstrap defaults apply
only to managed TMT VMs, not other frameworks or execution strategies.
