# Test an existing image with pytest and LISA

Start with a disk image you already have. First check the file using pytest on
the host; then boot a disposable copy with LISA and assert its OS identity inside
the VM. Neither step rebuilds the image.

These are different kinds of tests: **pytest does not boot the image**. An image
can pass the file check and still fail to boot. For TMT image testing, see the
[TMT variant](#use-a-tmt-plan-instead).

## Create an image-test project

You need `azldev`, Python 3.11 or newer with venv/pip support, and network access
to install the example's pytest dependency. No virtualization is needed for
this first step.

```sh
mkdir -p image-test/tests/image
cd image-test
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

[images.candidate.tests]
tests = [{ name = "image-bytes" }]

[tests.image-bytes]
type = "pytest"
pytest = { working-dir = "tests/image", test-paths = ["test_image.py"], install = "requirements" }

[tests.image-boot]
type = "lisa"
lisa = { testcase-name = "verify_azure_linux", pip-extras = ["libvirt", "azure"] }
```

`candidate` is a local name for the subject, not a filename or a build recipe.
Even with `--image-path`, an image name must exist in project configuration or the
selected distro's published catalog. An empty image definition with test
references is sufficient; no KIWI file is required.

`image-bytes` is the default associated test. `image-boot` is configured but will
run only when explicitly selected below. Its framework source will come from
`--lisa-dir`.

## Inspect the artifact with pytest

Create `tests/image/requirements.txt`:

```text
pytest==9.0.2
```

Create `tests/image/test_image.py`:

```python
import hashlib
import json
import os
from pathlib import Path


def test_image_snapshot():
    manifest = json.loads(Path(os.environ["AZLDEV_TEST_INPUTS"]).read_text())
    assert manifest["kind"] == "image"
    image = manifest["inputs"]["image"]
    path = Path(image["path"])
    assert path.is_file()
    assert path.stat().st_size > 0
    assert path.stat().st_mode & 0o222 == 0
    assert image["originalPath"] != image["path"]
    with path.open("rb") as stream:
        assert hashlib.file_digest(stream, "sha256").hexdigest() == image["sha256"]
```

This test checks that the selected file is nonempty and that the read-only input
snapshot matches its recorded digest. It does **not** establish that the contents
are a valid disk image, the expected release, or trusted by a publisher. Extend
it with your artifact-specific assertions; avoid loading multi-gigabyte images
entirely into memory.

Set `IMAGE` to your existing disk image and run:

```sh
IMAGE="/absolute/path/to/azurelinux-4.x86_64.qcow2"
azldev test run --image candidate --image-path "$IMAGE" \
  --arch x86_64 --junit-xml image-bytes.xml
```

Expect one passed case and exit code 0. azldev copies the test sources, installs
`requirements.txt` into a private environment, runs pytest, and retains reports
before removing the environment and input snapshot. Your source tree and image
are not modified.

Do not omit `install = "requirements"` unless you intend to prepare the environment
yourself. The default `install = "none"` requires a preexisting
`work/pytest-venv/image-bytes` venv with pytest installed; it does not mean
"automatically use system pytest." See [install modes](../reference/config/tests.md#install-modes).

## Boot and inspect the guest with LISA

The **host** now needs working system libvirt/QEMU/KVM, `qemu-img`, UEFI firmware,
`ssh-keygen`, Python build prerequisites for the libvirt binding, Git, and network
access. Check access as the same user that runs azldev:

```sh
test -r /dev/kvm && test -w /dev/kvm
virsh -c qemu:///system list --all
```

These checks are prerequisites, not a complete host setup. Distribution packages,
libvirt service names, group membership, and network configuration vary. Do not
disable libvirt authentication on a shared host to make a test pass.

The **image** must match `--arch`, support cloud-init on QEMU and SSH key injection,
and permit passwordless sudo for the `lisa` account created for the test.
LISA must finish provisioning
before the case can run. Existing Azure-optimized images sometimes need the
[QEMU-specific copy below](#prepare-a-qemu-compatible-copy).

Use this pinned LISA revision, then add a small local suite:

```sh
mkdir -p tools
git clone --no-checkout https://github.com/microsoft/lisa.git tools/lisa
git -C tools/lisa checkout --detach 1d5bd3677082c382637e0edc5c2a9443017cb350
```

Create `tools/lisa/lisa/microsoft/testsuites/azldev_image_smoke.py`
(the first `lisa` is the checkout directory; the second is its Python package):

```python
from lisa import Node, TestCaseMetadata, TestSuite, TestSuiteMetadata


@TestSuiteMetadata(
    area="image",
    category="functional",
    owner="image-team",
    description="Check an Azure Linux 4 guest",
)
class ImageSmoke(TestSuite):
    @TestCaseMetadata(description="Verify guest OS identity", priority=0)
    def verify_azure_linux(self, node: Node) -> None:
        node.execute(
            'set -eu; . /etc/os-release; test "$ID" = azurelinux; test "$VERSION_ID" = 4 -o "$VERSION_ID" = 4.0',
            shell=True,
        ).assert_exit_code()
```

Run the explicitly selected test:

```sh
azldev test run --image candidate --image-path "$IMAGE" \
  --arch x86_64 --test image-boot --lisa-dir tools/lisa \
  --junit-xml image-boot.xml
```

`--test image-boot` **replaces**, rather than adds to, the image's default test
selection. The local LISA checkout, including the uncommitted suite, is copied
into a private runtime. The `libvirt` and `azure` extras satisfy this pinned
revision's platform/test-collection dependencies; installing the Azure extra
does not switch the run to Azure or require an Azure subscription.

Expect `ImageSmoke.verify_azure_linux` to pass after the VM boots. The managed VM
and generated administrative key are cleaned up, while the result directory and
native logs remain. A provisioning or cloud-init failure is an infrastructure
error, not a failed OS-identity assertion. To share this suite with a team,
commit it to your LISA fork and configure `lisa.source` with the fork URL and a
full commit SHA instead of relying on an uncommitted local file.

Run both checks together by repeating the selector:

```sh
azldev test run --image candidate --image-path "$IMAGE" --arch x86_64 \
  --test image-bytes --test image-boot --lisa-dir tools/lisa \
  --junit-xml image-tests.xml
```

Do not add `--execution vm` to that mixed run: pytest is host-side, while LISA's
default is VM. A global execution override must be supported by every selected
test.

## Prepare a QEMU-compatible copy

Use this only when the original image enables cloud-init's Hyper-V telemetry
reporter and fails under QEMU because `/var/lib/hyperv/.kvp_pool_1` is absent.
Inspect the native provisioning/cloud-init logs first; this does not fix other
cloud-init errors.

Keep the original unchanged. In the same project, create a customization directory
and a **different output directory**:

```sh
mkdir -p qemu-config out work
```

Create `qemu-config/qemu-reporting.cfg`:

```yaml
reporting:
  telemetry:
    type: log
```

Create `qemu-config/config.yaml`:

```yaml
previewFeatures:
  - preview-distro-version
os:
  additionalFiles:
    - source: qemu-reporting.cfg
      destination: /etc/cloud/cloud.cfg.d/99-qemu-reporting.cfg
```

Append this tool selection to `azldev.toml`:

```toml
[tools.imageCustomizer]
containerTag = "ghcr.io/microsoft/imagecustomizer:1.6.0"
```

Image Customizer requires Docker and permission to run its privileged image
customization container. It changes the copy's cloud-init reporting policy; this
is not a workaround that suppresses arbitrary provisioning errors.
Use absolute paths for its input, config, and output: unlike the test commands,
the customizer currently passes these paths directly to Docker bind mounts.

```sh
azldev image customize --image-file "$IMAGE" \
  --image-config "$PWD/qemu-config/config.yaml" \
  --output-image-format qcow2 --output-path "$PWD/out/qemu-test.qcow2"

azldev test run --image candidate --image-path out/qemu-test.qcow2 \
  --arch x86_64 --test image-boot --lisa-dir tools/lisa \
  --junit-xml image-boot-qemu.xml
```

The test now describes the **customized copy**, not the exact original artifact.
For products that must support both Hyper-V and QEMU, fix and test the image's
supported boot configurations rather than silently applying this customization
in every test run.

## Use a TMT plan instead

Copy the fmf layout from the [component walkthrough](test-component.md) into
`tests/image-tmt`, using the same plan and `.fmf/version`. Replace the package
test's `main.fmf` with:

```yaml
summary: Verify the guest OS identity
test: |
  set -eu
  . /etc/os-release
  test "$ID" = azurelinux
```

Add a named TMT test to this project's catalog:

```toml
[tests.image-tmt]
type = "tmt"
[tests.image-tmt.tmt]
plan = "/plans/smoke"
cloud-init-runcmds = [
  "if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then firewall-cmd --add-service=ssh && firewall-cmd --add-port=10022/tcp; fi",
]
```

```sh
azldev test run --image candidate --image-path "$IMAGE" --arch x86_64 \
  --test image-tmt --source-dir tests/image-tmt
```

The image itself is the subject; there is no separate base image and no candidate
RPM installation. Use an OCI image and `--execution container` for container
image tests, not a qcow2 disk.

## Move to project-managed images

In a project with real image recipes and test associations:

```sh
azldev test run --image vm-base                  # reuse a local image, build if missing
azldev test run --image vm-base --rebuild        # always build before testing
azldev test run --image vm-base --artifact-source lkg
```

The LKG form requires the distro's
[published artifact catalog](../reference/config/distros.md#published-test-artifacts).
It does not discover cloud URLs automatically. With the standalone `candidate`
definition, continue supplying `--image-path`; there is no recipe to build.

See [Automate artifact tests](automate-artifact-tests.md) for reliable report
discovery, exit status handling, and CI publishing.
