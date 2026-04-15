# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Example tests: validate required filesystem structure in a mounted image.

These tests demonstrate how to use the azldev_check fixtures to verify
that an image contains the expected directory layout and key files.
"""

from __future__ import annotations

import pytest

# Required top-level directories that every Azure Linux image should contain.
REQUIRED_DIRS = [
    "/bin",
    "/etc",
    "/lib",
    "/sbin",
    "/usr",
    "/var",
]

# Required files that should always be present.
REQUIRED_FILES = [
    "/etc/os-release",
]

# Paths that must not exist in a production image.
FORBIDDEN_PATHS = [
    "/etc/shadow-",
    "/root/.bash_history",
]


@pytest.mark.filesystem
class TestRequiredStructure:
    """Check that the image has the expected directory layout."""

    @pytest.mark.parametrize("expected_dir", REQUIRED_DIRS)
    def test_required_directory_exists(
        self, image_file_paths: set[str] | None, expected_dir: str
    ) -> None:
        """Verify that a required directory exists in the image."""
        if image_file_paths is None:
            pytest.skip("No image mounted (--azldev-image not provided)")

        assert expected_dir in image_file_paths, (
            f"Required directory {expected_dir!r} not found in image"
        )

    @pytest.mark.parametrize("expected_file", REQUIRED_FILES)
    def test_required_file_exists(
        self, image_file_paths: set[str] | None, expected_file: str
    ) -> None:
        """Verify that a required file exists in the image."""
        if image_file_paths is None:
            pytest.skip("No image mounted (--azldev-image not provided)")

        assert expected_file in image_file_paths, (
            f"Required file {expected_file!r} not found in image"
        )


@pytest.mark.filesystem
class TestForbiddenPaths:
    """Check that no forbidden paths exist in the image."""

    @pytest.mark.parametrize("forbidden_path", FORBIDDEN_PATHS)
    def test_forbidden_path_absent(
        self, image_file_paths: set[str] | None, forbidden_path: str
    ) -> None:
        """Verify that a forbidden path does not exist in the image."""
        if image_file_paths is None:
            pytest.skip("No image mounted (--azldev-image not provided)")

        assert forbidden_path not in image_file_paths, (
            f"Forbidden path {forbidden_path!r} found in image"
        )
