# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Example tests: validate package inventory from a kiwi .packages manifest.

These tests demonstrate how to use the manifest fixture to verify that
expected packages are installed and unwanted packages are absent.
"""

from __future__ import annotations

import pytest

from azldev_check.types import PackageInfo

# Packages that must be present in every Azure Linux image.
REQUIRED_PACKAGES = [
    "systemd",
    "filesystem",
]

# Packages that must NOT be present in a production image.
FORBIDDEN_PACKAGES = [
    "valgrind",
    "strace",
]


@pytest.mark.packages
class TestRequiredPackages:
    """Verify that required packages are present in the manifest."""

    @pytest.mark.parametrize("expected_pkg", REQUIRED_PACKAGES)
    def test_required_package_installed(
        self, package_names: set[str] | None, expected_pkg: str
    ) -> None:
        """Check that a required package appears in the manifest."""
        if package_names is None:
            pytest.skip("No manifest provided (--azldev-manifest not given)")

        assert expected_pkg in package_names, (
            f"Required package {expected_pkg!r} not found in image manifest"
        )


@pytest.mark.packages
class TestForbiddenPackages:
    """Verify that unwanted packages are absent from the manifest."""

    @pytest.mark.parametrize("forbidden_pkg", FORBIDDEN_PACKAGES)
    def test_forbidden_package_absent(
        self, package_names: set[str] | None, forbidden_pkg: str
    ) -> None:
        """Check that a forbidden package is not in the manifest."""
        if package_names is None:
            pytest.skip("No manifest provided (--azldev-manifest not given)")

        assert forbidden_pkg not in package_names, (
            f"Forbidden package {forbidden_pkg!r} found in image manifest"
        )


@pytest.mark.packages
class TestPackageMetadata:
    """Validate metadata across all packages in the manifest."""

    def test_all_packages_have_architecture(
        self, manifest: list[PackageInfo] | None
    ) -> None:
        """Check that every package has a non-empty architecture field."""
        if manifest is None:
            pytest.skip("No manifest provided")

        bad = [p.name for p in manifest if not p.arch or p.arch == "(none)"]
        assert not bad, f"Packages missing architecture: {bad}"

    def test_no_duplicate_package_names(
        self, manifest: list[PackageInfo] | None
    ) -> None:
        """Check for duplicate package name+arch combinations."""
        if manifest is None:
            pytest.skip("No manifest provided")

        seen: dict[str, int] = {}
        for pkg in manifest:
            key = f"{pkg.name}.{pkg.arch}"
            seen[key] = seen.get(key, 0) + 1

        dupes = {k: v for k, v in seen.items() if v > 1}
        assert not dupes, f"Duplicate packages: {dupes}"
