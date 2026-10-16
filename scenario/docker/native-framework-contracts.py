#!/usr/bin/env python3
# Copyright (c) 2026 Microsoft Corporation.
# Licensed under the MIT License.
"""Exercise real framework contracts inside an owned, disposable container."""

# Assertions are the test oracle, not production input validation.
# ruff: noqa: S101
from __future__ import annotations

import functools
import hashlib
import http.server
import json
import os
import shutil
import signal
import subprocess
import sys
import threading
import time
import xml.etree.ElementTree as ET
from pathlib import Path

FIXTURE = Path("/workdir/fixture")
ROOT = Path(os.environ["AZLDEV_NATIVE_WORK"])
RPM = Path(os.environ.get("AZLDEV_NATIVE_RPM", "/opt/azldev-tmt-fixtures/candidate.rpm"))
SOURCE = ROOT / "source"
PROJECT = ROOT / "project"
WORK = ROOT / "project-work"
TIMINGS: dict[str, float] = {}


def command(args: list[str], *, cwd: Path | None = None) -> str:
    """Run a fixture command, retaining its diagnostic output on failure."""
    result = subprocess.run(args, cwd=cwd, text=True, capture_output=True, check=False)
    assert result.returncode == 0, (args, result.stdout, result.stderr)
    return result.stdout.strip()


def digest(path: Path) -> str:
    """Hash a fixture without loading a disk image into memory."""
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def prepare_project(base_url: str) -> None:
    """Create a small project and a pinned local test-source repository."""
    shutil.copytree(FIXTURE / "source", SOURCE)
    for template in SOURCE.rglob("*.py.in"):
        template.rename(template.with_suffix(""))
    command(["git", "init", "--quiet", str(SOURCE)])
    command(["git", "add", "."], cwd=SOURCE)
    command(
        [
            "git",
            "-c",
            "user.name=Fixture",
            "-c",
            "user.email=fixture@example.invalid",
            "commit",
            "--quiet",
            "-m",
            "fixture",
        ],
        cwd=SOURCE,
    )
    revision = command(["git", "rev-parse", "HEAD"], cwd=SOURCE)
    PROJECT.mkdir()
    (PROJECT / "probe.spec").write_text(
        "Name: azldev-tmt-probe\nVersion: 1.0\nRelease: 1\nSummary: Probe\nLicense: MIT\n%description\nProbe\n%files\n",
    )
    sample = ROOT / "http" / "sample.qcow2"
    command(["qemu-img", "create", "-f", "qcow2", str(sample), "8M"])
    shutil.copyfile(RPM, ROOT / "http" / "candidate.rpm")
    published_rpm = f"{base_url}/candidate.rpm"
    container = (FIXTURE / "container-reference").read_text().strip()
    lines = [
        "[project]",
        'default-distro = {name="azurelinux", version="4.0"}',
        f"work-dir = {json.dumps(str(WORK))}",
        f"output-dir = {json.dumps(str(ROOT / 'out'))}",
        f"log-dir = {json.dumps(str(ROOT / 'logs'))}",
        '[distros.azurelinux.versions."4.0"]',
        'release-ver = "4.0"',
        'test-base-images = {container="container-base"}',
        '[distros.azurelinux.versions."4.0".images.container-base]',
        f'architectures.x86_64 = [{{format="oci", uri={json.dumps("docker://" + container)}}}]',
        '[distros.azurelinux.versions."4.0".images.sample]',
        f'architectures.x86_64 = [{{format="qcow2", uri="{base_url}/sample.qcow2", sha256="{digest(sample)}"}}]',
        '[distros.azurelinux.versions."4.0".test-packages.azldev-tmt-probe]',
        f'architectures.x86_64 = [{{uri="{published_rpm}", sha256="{digest(RPM)}"}}]',
        "[components.azldev-tmt-probe.spec]",
        'type = "local"',
        'path = "probe.spec"',
    ]
    for name in ["sample", "vm"]:
        lines.extend([f"[images.{name}]", 'definition = {type="kiwi", path="unused.kiwi"}'])
    for name, plan in [
        ("smoke", "candidate"),
        ("skip", "skip"),
        ("fail", "fail"),
        ("cancel", "cancel"),
        ("empty", "empty"),
    ]:
        lines.extend(
            [
                f"[tests.tmt-{name}]",
                'type = "tmt"',
                f"[tests.tmt-{name}.tmt]",
                f'source = {{git-url={json.dumps(SOURCE.as_uri())}, ref="{revision}"}}',
                f'plan = "/plans/{plan}"',
            ]
        )
    for name in ["pass", "skip", "fail", "error", "empty", "legacy"]:
        test_path = "empty" if name == "empty" else f"cases/{'pass' if name == 'legacy' else name}_case.py"
        lines.extend(
            [
                f"[tests.pytest-{name}]",
                'type = "pytest"',
                f"[tests.pytest-{name}.pytest]",
                f'source = {{git-url={json.dumps(SOURCE.as_uri())}, ref="{revision}"}}',
                'working-dir = "pytest"',
                f'test-paths = ["{test_path}"]',
                f'install = "{"none" if name == "legacy" else "requirements"}"',
            ]
        )
    for name, cases in [
        ("candidate", ["verify_candidate"]),
        ("image", ["verify_image"]),
        ("mixed", ["verify_candidate", "expected_failure", "expected_skip"]),
        ("skip", ["expected_skip"]),
        ("empty", ["does_not_exist"]),
    ]:
        lines.extend(
            [
                f"[tests.lisa-{name}]",
                'type = "lisa"',
                f"[tests.lisa-{name}.lisa]",
                'pip-extras = ["libvirt", "azure"]',
                f"testcase-names = {json.dumps(cases)}",
            ]
        )
    (PROJECT / "azldev.toml").write_text("\n".join(lines) + "\n")
    local_rpms = ROOT / "out" / "rpms" / "rpm-base"
    local_rpms.mkdir(parents=True)
    shutil.copyfile(RPM, local_rpms / "candidate.noarch.rpm")
    local_images = ROOT / "out" / "images" / "sample"
    local_images.mkdir(parents=True)
    shutil.copyfile(sample, local_images / "sample.x86_64.qcow2")


def run_case(
    name: str,
    arguments: list[str],
    expected: str,
    *,
    source: str = "path",
    cancel_marker: str = "",
) -> dict[str, object]:
    """Check actual outcomes, native cases, provenance, input preservation and cleanup."""
    work = ROOT / "runs" / name
    report = ROOT / f"{name}.xml"
    log = ROOT / f"{name}.log"
    before = digest(RPM)
    env = dict(os.environ, EXPECTED_ARTIFACT_SOURCE=source)
    started = time.monotonic()
    args = ["azldev", "-C", str(PROJECT), *arguments, "--work-dir", str(work), "--junit-xml", str(report)]
    if cancel_marker:
        args.insert(1, "--verbose")
    with log.open("w") as output:
        process = subprocess.Popen(args, stdout=output, stderr=subprocess.STDOUT, env=env)
        try:
            if cancel_marker:
                deadline = time.monotonic() + 300
                while cancel_marker not in log.read_text(errors="replace"):
                    assert process.poll() is None, log.read_text(errors="replace")
                    assert time.monotonic() < deadline, f"native stage not reached: {log}"
                    time.sleep(0.25)
                process.send_signal(signal.SIGINT)
            code = process.wait(timeout=900)
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
    TIMINGS[name] = time.monotonic() - started
    assert (code == 0) == (expected in {"passed", "skipped"}), log.read_text(errors="replace")
    result = verify_results(work, report, expected)
    verify_snapshot_cleanup(work)
    assert digest(RPM) == before, "candidate RPM modified"
    assert not command(["git", "status", "--porcelain"], cwd=SOURCE), "caller test sources modified"
    print(f"{name}: {expected} in {TIMINGS[name]:.1f}s", flush=True)
    return result


def verify_results(work: Path, report: Path, expected: str) -> dict[str, object]:
    """Check normalized results and native JUnit without relying on stdout formatting."""
    records = list(work.glob("tests/*/results.json"))
    assert len(records) == 1, (work, records)
    results = json.loads(records[0].read_text())
    assert len(results) == 1, results
    assert results[0]["status"] == expected, results
    result = results[0]
    if expected in {"passed", "skipped", "failed"}:
        assert result["cases"], result
        assert all(case["nativeStatus"] for case in result["cases"]), result
        environment = Path(result["outputDir"]) / "python-environment.txt"
        assert environment.is_file(), result
        assert "# Python " in environment.read_text(), environment
    xml = ET.parse(report).getroot()  # noqa: S314 - only reports produced by these owned fixtures
    if expected == "skipped":
        assert xml.findall(".//skipped"), ET.tostring(xml)
        assert not xml.findall(".//failure"), ET.tostring(xml)
    if expected == "failed":
        assert xml.findall(".//failure"), ET.tostring(xml)
    if expected in {"error", "cancelled"}:
        assert xml.findall(".//error"), ET.tostring(xml)
    return result


def verify_snapshot_cleanup(work: Path) -> None:
    """Check that manifests retain identity while private snapshots are released."""
    for manifest in work.glob("tests/*/*/inputs.json"):
        inputs = json.loads(manifest.read_text())["inputs"]
        artifacts = inputs.get("packages", [])
        if inputs.get("image"):
            artifacts.append(inputs["image"])
        for item in artifacts:
            if item.get("path"):
                assert item["originalPath"], item
                assert item["sha256"], item
                assert not Path(item["path"]).exists(), f"snapshot leaked: {item}"
            else:
                assert item["ociImageId"].startswith("sha256:"), item


def local_contracts() -> None:
    """Exercise native pytest plus TMT host, container and SSH strategies."""
    component = ["component", "test", "azldev-tmt-probe"]
    explicit = ["--rpm", str(RPM)]
    for origin, paths in [("path", explicit), ("local", []), ("lkg", ["--artifact-source", "lkg"])]:
        run_case(f"pytest-component-{origin}", [*component, "--test", "pytest-pass", *paths], "passed", source=origin)
        image_args = ["--image-path", str(ROOT / "http" / "sample.qcow2")] if origin == "path" else []
        if origin == "lkg":
            image_args = ["--artifact-source", "lkg"]
        run_case(
            f"pytest-image-{origin}",
            ["image", "test", "sample", "--test", "pytest-pass", *image_args],
            "passed",
            source=origin,
        )
    for name, status in [("skip", "skipped"), ("fail", "failed"), ("error", "error"), ("empty", "error")]:
        run_case(f"pytest-{name}", [*component, "--test", f"pytest-{name}", *explicit], status)
    prepared = WORK / "pytest-venv" / "pytest-legacy"
    command(["python3", "-m", "venv", str(prepared)])
    command([str(prepared / "bin/python3"), "-m", "pip", "install", "-r", str(SOURCE / "pytest/requirements.txt")])
    config_hash = digest(prepared / "pyvenv.cfg")
    run_case("pytest-main-compatibility", [*component, "--test", "pytest-legacy", *explicit], "passed")
    assert digest(prepared / "pyvenv.cfg") == config_hash

    command(["sudo", "tdnf", "-y", "install", str(RPM)])
    command(["sudo", "sh", "-c", "printf 'stale same-version payload\\n' >/usr/bin/azldev-tmt-probe"])
    run_case("tmt-host", [*component, "--test", "tmt-smoke", "--execution", "host", *explicit], "passed")
    run_case("tmt-skip", [*component, "--test", "tmt-skip", "--execution", "host", *explicit], "skipped")
    run_case("tmt-fail", [*component, "--test", "tmt-fail", "--execution", "host", *explicit], "failed")
    run_case("tmt-empty", [*component, "--test", "tmt-empty", "--execution", "host", *explicit], "error")
    run_case("tmt-container", [*component, "--test", "tmt-smoke", "--execution", "container", *explicit], "passed")
    run_case(
        "tmt-container-cancel",
        [*component, "--test", "tmt-cancel", "--execution", "container", *explicit],
        "cancelled",
        cancel_marker="summary: 1 guest provisioned",
    )
    assert not command(["podman", "ps", "-aq"]), "managed containers leaked"
    ssh_contracts(component, explicit)


def ssh_contracts(component: list[str], explicit: list[str]) -> None:
    """Keep an existing SSH target alive after successful and cancelled tests."""
    key = ROOT / "client-key"
    command(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key)])
    bootstrap = (
        "tdnf -y install openssh-server rsync sudo shadow-utils; "
        "useradd -m testuser; mkdir -p /etc/sudoers.d; "
        "printf 'testuser ALL=(ALL) NOPASSWD: ALL\\n' >/etc/sudoers.d/testuser; "
        "ssh-keygen -A; exec /usr/sbin/sshd -D -e "
        "-o KexAlgorithms=curve25519-sha256 -o PasswordAuthentication=no "
        "-o PermitRootLogin=no -o AuthorizedKeysFile=/keys/client.pub"
    )
    target = command(
        [
            "podman",
            "run",
            "--detach",
            "--publish",
            "127.0.0.1::22",
            "--volume",
            f"{key}.pub:/keys/client.pub:ro",
            (FIXTURE / "container-reference").read_text().strip(),
            "bash",
            "-ceu",
            bootstrap,
        ]
    )
    try:
        port = command(["podman", "port", target, "22/tcp"]).rsplit(":", 1)[1]
        known_hosts = ROOT / "known-hosts"
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            scan = subprocess.run(
                ["ssh-keyscan", "-T", "2", "-p", port, "127.0.0.1"],
                capture_output=True,
                text=True,
                check=False,
            )
            if scan.returncode == 0:
                known_hosts.write_text(scan.stdout)
                break
            time.sleep(0.25)
        assert known_hosts.exists(), command(["podman", "logs", target])
        options = [
            "--execution",
            "ssh",
            "--ssh-host",
            "127.0.0.1",
            "--ssh-user",
            "testuser",
            "--ssh-key",
            str(key),
            "--ssh-port",
            port,
        ]
        run_case("tmt-ssh", [*component, "--test", "tmt-smoke", *options, *explicit], "passed")
        run_case(
            "tmt-ssh-cancel",
            [*component, "--test", "tmt-cancel", *options, *explicit],
            "cancelled",
            cancel_marker="cmd: echo AZLDEV_NATIVE_CANCEL_READY;",
        )
        command(
            [
                "ssh",
                "-o",
                "BatchMode=yes",
                "-o",
                "StrictHostKeyChecking=yes",
                "-o",
                f"UserKnownHostsFile={known_hosts}",
                "-i",
                str(key),
                "-p",
                port,
                "testuser@127.0.0.1",
                "true",
            ]
        )
    finally:
        command(["podman", "rm", "--force", target])
        key.unlink()
        key.with_suffix(".pub").unlink()


def lisa_contracts() -> None:
    """Run real LISA candidate, image and mixed-result cases on an isolated libvirt host."""
    framework = ROOT / "lisa"
    revision = (FIXTURE / "lisa-revision").read_text().strip()
    (ROOT / "lisa-revision.txt").write_text(revision + "\n")
    command(["git", "init", "--quiet", str(framework)])
    command(["git", "remote", "add", "origin", "https://github.com/microsoft/lisa.git"], cwd=framework)
    command(["git", "fetch", "--depth=1", "origin", revision], cwd=framework)
    command(["git", "checkout", "--detach", "FETCH_HEAD"], cwd=framework)
    shutil.copyfile(FIXTURE / "lisa-smoke.py.in", framework / "lisa/microsoft/testsuites/azldev_contract.py")
    before = command(["git", "status", "--porcelain"], cwd=framework)
    image = "/inputs/vm-base.qcow2"
    common = ["--lisa-dir", str(framework)]
    run_case(
        "lisa-component",
        [
            "component",
            "test",
            "azldev-tmt-probe",
            "--test",
            "lisa-candidate",
            "--base-image-path",
            image,
            "--rpm",
            str(RPM),
            *common,
        ],
        "passed",
    )
    run_case("lisa-image", ["image", "test", "vm", "--test", "lisa-image", "--image-path", image, *common], "passed")
    result = run_case(
        "lisa-mixed",
        [
            "component",
            "test",
            "azldev-tmt-probe",
            "--test",
            "lisa-mixed",
            "--base-image-path",
            image,
            "--rpm",
            str(RPM),
            *common,
        ],
        "failed",
    )
    cases = result["cases"]
    assert isinstance(cases, list)
    assert {case["status"] for case in cases} == {"passed", "failed", "skipped"}
    run_case(
        "lisa-skip",
        ["image", "test", "vm", "--test", "lisa-skip", "--image-path", image, *common],
        "skipped",
    )
    run_case(
        "lisa-empty",
        ["image", "test", "vm", "--test", "lisa-empty", "--image-path", image, *common],
        "error",
    )
    invalid_rpm = ROOT / "invalid.rpm"
    invalid_rpm.write_text("intentionally not a valid RPM")
    result = run_case(
        "lisa-setup-error",
        [
            "component",
            "test",
            "azldev-tmt-probe",
            "--test",
            "lisa-candidate",
            "--base-image-path",
            image,
            "--rpm",
            str(invalid_rpm),
            *common,
        ],
        "error",
    )
    cases = result["cases"]
    assert isinstance(cases, list)
    assert any(case["status"] == "error" and case["nativeStatus"] == "failure" for case in cases), result
    assert command(["git", "status", "--porcelain"], cwd=framework) == before
    assert not command(["virsh", "-c", "qemu:///system", "list", "--all", "--name"]), "LISA guests leaked"


def main() -> None:
    """Run one native backend family and retain machine-readable timing evidence."""
    ROOT.mkdir(parents=True, exist_ok=True)
    (ROOT / "http").mkdir()
    handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=str(ROOT / "http"))
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        prepare_project(f"http://127.0.0.1:{server.server_port}")
        if sys.argv[1] == "local":
            local_contracts()
        else:
            assert sys.argv[1] == "lisa"
            lisa_contracts()
    finally:
        server.shutdown()
        server.server_close()
        thread.join()
        (ROOT / "timings.json").write_text(json.dumps(TIMINGS, indent=2) + "\n")


if __name__ == "__main__":
    main()
