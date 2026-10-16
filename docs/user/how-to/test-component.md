# Test a component's RPMs in a TMT VM

This walkthrough installs an existing Azure Linux `zlib-ng-compat` RPM into a
disposable VM, checks its installed files, and exercises compression and
decompression. It does not build the RPM or supporting image, and it does not
install the candidate package on your development host.

For artifact-selection policies and other frameworks, see
[Test build artifacts](test-build-artifacts.md).

## Prerequisites

On the **host**, you need:

- `azldev`, Git, Python 3.12 or newer with venv/pip support, and network access
  to install azldev's pinned TMT runtime.
- QEMU/KVM, libvirt, `qemu-img`, UEFI firmware, SSH tools, rsync, and a cloud-init
  ISO tool such as `genisoimage`. Your user must have access to `/dev/kvm` and a
  working `qemu:///session` connection.
- A C compiler, Python development headers, `pkg-config`, and libvirt development
  files if pip must build the libvirt Python binding.

The **guest image** must match the RPM's architecture and Azure Linux release,
support cloud-init and SSH key injection, and allow its test user passwordless
sudo. It needs working package repositories for test dependencies. This example
uses an x86_64 Azure Linux 4 image and the `azureuser` account. The image's
cloud-init configuration must also work under QEMU, not just Hyper-V; see
[preparing a QEMU-compatible copy](test-image.md#prepare-a-qemu-compatible-copy).

Use trusted test sources and images: a disposable guest is not a sandbox for
untrusted framework code running on the host.

## Create a small test project

Create a new directory, separate from your packaging checkout:

```sh
mkdir -p component-test/tests/zlib/.fmf \
  component-test/tests/zlib/plans component-test/tests/zlib/tests/roundtrip
cd component-test
```

Create `azldev.toml`:

```toml
[project]
default-distro = { name = "azurelinux", version = "4.0" }
work-dir = "work"
output-dir = "out"
log-dir = "logs"

[distros.azurelinux.versions."4.0"]
release-ver = "4.0"

[components.zlib-ng.tests]
tests = [{ name = "zlib-roundtrip" }]

[tests.zlib-roundtrip]
type = "tmt"

[tests.zlib-roundtrip.tmt]
plan = "/plans/smoke"
cloud-init-runcmds = [
  "if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then firewall-cmd --add-service=ssh && firewall-cmd --add-port=10022/tcp; fi",
]
```

This project defines only the subject and its tests, not a spec or a build
recipe. **The standalone commands below use the preview
`--without-lockfile` mode.** In ordinary mode, component resolution still checks
upstream lock files even when you supply an explicit RPM. Do not create a dummy
spec or lock file to disguise that requirement. In an existing packaging project,
use that project's normal lock-file mode instead.

The bootstrap command runs **inside the new guest**, before readiness checks.
It opens SSH and testcloud's session-mode TCP 10022 listener when firewalld is
active; it does not change the host firewall. The rules are not permanent and
last until reboot. Other guest firewall systems need their own configuration.

## Write the plan and test

Mark the metadata root:

```sh
printf '1\n' > tests/zlib/.fmf/version
```

Create `tests/zlib/plans/smoke.fmf`:

```yaml
summary: Check the installed zlib-compatible package
discover:
  how: fmf
provision:
  how: virtual
  connection: session
  user: azureuser
execute:
  how: tmt
```

Create `tests/zlib/tests/roundtrip/main.fmf`:

```yaml
summary: Verify candidate package files and compression round trip
require: [python3]
test: |
  set -eu
  rpm -q zlib-ng-compat
  rpm -V zlib-ng-compat
  python3 - <<'PY'
  import zlib
  data = b"azure linux artifact test\n" * 1000
  compressed = zlib.compress(data)
  assert len(compressed) < len(data)
  assert zlib.decompress(compressed) == data
  print("zlib runtime:", zlib.ZLIB_RUNTIME_VERSION)
  PY
```

TMT runs this script in the guest, after azldev's candidate RPM installation.
`require` installs the guest-side test dependency; it does not install host tools.
This is a smoke test of the compatibility package and Python's zlib consumer,
not a substitute for the full zlib-ng upstream suite.

## Run against explicit artifacts

Set these to existing files. The RPM below is an example of output from a real
Azure Linux checkout; adjust the release filename to your build:

```sh
RPM="$HOME/src/azurelinux/base/out/rpms/rpm-base/zlib-ng-compat-2.3.3-6.azl4.x86_64.rpm"
BASE_IMAGE="/absolute/path/to/azurelinux-4.x86_64.qcow2"

azldev --without-lockfile component test zlib-ng \
  --execution vm --arch x86_64 \
  --rpm "$RPM" \
  --base-image-path "$BASE_IMAGE" \
  --source-dir tests/zlib \
  --junit-xml results.xml
```

Here `zlib-ng` is the **source component name**; `zlib-ng-compat` is one of its
binary packages. Only the explicit RPM is selected. Add more `--rpm` options if
the test needs other compatible subpackages; avoid a blanket glob that includes
conflicting variants or source RPMs.

The expected outcome is one passed TMT case, a `passed` command result, and exit
code 0. The VM is removed. The original RPM and image are unchanged; azldev
consumes private snapshots and removes those after the run. Results remain in
`work/tests/run-*/results.json`, with the native TMT logs under the same run
directory and the aggregate report at `results.xml`. See
[automation and failure handling](automate-artifact-tests.md).

Add global `--dry-run` to inspect selection without provisioning. A dry run does
not prove the image boots, the Python runtime installs, or the plan succeeds.

## Use the test in an existing packaging project

Add the named test to the project's test catalog and the reference to its existing
`[components.zlib-ng.tests]` table. Do not create a second component definition.
Put the fmf tree under a test-source directory separate from the project's work
directory, or configure a [pinned TMT source](../reference/config/tests.md#tmt-fields).

With an existing `vm-base` image definition and associated test:

```sh
# Reuse local RPMs, building them if missing; fetch the published base image.
azldev component test zlib-ng --base-image vm-base --source-dir tests/zlib

# Reuse a local base image too, building it from the local recipe if missing.
azldev component test zlib-ng --base-image vm-base \
  --base-image-source local --source-dir tests/zlib

# Force a new component build, without rebuilding the published base.
azldev component test zlib-ng --rebuild --base-image vm-base --source-dir tests/zlib
```

These examples require the project's real build configuration or
[published image catalog](../reference/config/distros.md#published-test-artifacts);
the standalone project above intentionally has neither. If the packaging project
uses lock-file-free mode, keep its usual `--without-lockfile` flag.

To run an existing upstream plan instead, pass its prepared fmf directory and
exact plan name, for example `--source-dir /path/to/prepared/zlib-ng --plan
/plans/build-gating`. The plan may discover tests from another repository.
Upstream tests may assume Fedora package names or behavior; an installation
failure is not evidence that the candidate's assertions failed. Inspect the
native log before deciding whether to adapt the test or fix the package.
