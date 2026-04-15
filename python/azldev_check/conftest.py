# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Pytest plugin providing fixtures for azldev image validation.

This module defines CLI options and session-scoped fixtures that make image
metadata, package manifests, and mounted filesystem state available to tests.

CLI options (injected by azldev when invoking pytest):
    --azldev-image      Path to the image file under test.
    --azldev-config      Path to the JSON image config context file.
    --azldev-manifest    Path to a kiwi .packages manifest file.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Generator

import pytest

from .imageaccess import list_files, mount_image
from .manifest import parse_packages_file
from .types import FileEntry, ImageConfig, PackageInfo


def pytest_configure(config: pytest.Config) -> None:
    """Register custom markers to avoid PytestUnknownMarkWarning."""
    config.addinivalue_line("markers", "packages: tests that validate the package manifest")
    config.addinivalue_line("markers", "filesystem: tests that validate the image filesystem")
    config.addinivalue_line("markers", "config: tests that validate the image config passthrough")


def pytest_addoption(parser: pytest.Parser) -> None:
    """Register azldev-specific command-line options."""
    group = parser.getgroup("azldev", "azldev image validation")

    group.addoption(
        "--azldev-image",
        dest="azldev_image",
        default=None,
        help="Path to the disk image file under test.",
    )
    group.addoption(
        "--azldev-config",
        dest="azldev_config",
        default=None,
        help="Path to the azldev image config JSON file.",
    )
    group.addoption(
        "--azldev-manifest",
        dest="azldev_manifest",
        default=None,
        help="Path to the kiwi .packages manifest file.",
    )


# ---------------------------------------------------------------------------
# Session-scoped fixtures
# ---------------------------------------------------------------------------


@pytest.fixture(scope="session")
def image_path(request: pytest.FixtureRequest) -> Path | None:
    """Return the path to the image file, or None if not provided."""
    val = request.config.getoption("azldev_image")
    return Path(val) if val else None


@pytest.fixture(scope="session")
def image_config(request: pytest.FixtureRequest) -> ImageConfig | None:
    """Parse and return the azldev image config, or None if not provided."""
    config_path = request.config.getoption("azldev_config")
    if not config_path:
        return None

    with open(config_path, encoding="utf-8") as f:
        data = json.load(f)

    return ImageConfig.from_dict(data)


@pytest.fixture(scope="session")
def manifest(request: pytest.FixtureRequest) -> list[PackageInfo] | None:
    """Parse and return the package manifest, or None if not provided."""
    manifest_path = request.config.getoption("azldev_manifest")
    if not manifest_path:
        return None

    return parse_packages_file(manifest_path)


@pytest.fixture(scope="session")
def package_names(manifest: list[PackageInfo] | None) -> set[str] | None:
    """Return the set of package names from the manifest, or None."""
    if manifest is None:
        return None

    return {pkg.name for pkg in manifest}


@pytest.fixture(scope="session")
def mounted_image(image_path: Path | None) -> Generator[Path | None, None, None]:
    """Mount the image read-only and yield the mountpoint, or yield None.

    Uses guestmount for offline read-only access. The image is unmounted
    and the temporary directory cleaned up after the test session.
    """
    if image_path is None:
        yield None
        return

    with mount_image(image_path) as mountpoint:
        yield mountpoint


@pytest.fixture(scope="session")
def image_files(mounted_image: Path | None) -> list[FileEntry] | None:
    """Return a list of all files in the mounted image, or None."""
    if mounted_image is None:
        return None

    return list_files(mounted_image)


@pytest.fixture(scope="session")
def image_file_paths(image_files: list[FileEntry] | None) -> set[str] | None:
    """Return the set of file paths in the image, or None."""
    if image_files is None:
        return None

    return {f.path for f in image_files}
