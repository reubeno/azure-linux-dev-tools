# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Parser for kiwi .packages manifest files.

The kiwi .packages file uses pipe-delimited fields:
    name|epoch|version|release|arch|unknown|license

Example line:
    alternatives|(none)|1.33|2.fc43|x86_64|(none)|GPL-2.0-only
"""

from __future__ import annotations

from pathlib import Path

from .types import PackageInfo

_EXPECTED_FIELDS = 7


def parse_packages_file(path: str | Path) -> list[PackageInfo]:
    """Parse a kiwi .packages manifest file into a list of PackageInfo objects.

    Args:
        path: Path to the .packages file.

    Returns:
        List of PackageInfo entries.

    Raises:
        ValueError: If a line has an unexpected number of fields.
        FileNotFoundError: If the file does not exist.
    """
    packages: list[PackageInfo] = []
    manifest_path = Path(path)

    with manifest_path.open(encoding="utf-8") as f:
        for line_num, raw_line in enumerate(f, start=1):
            line = raw_line.strip()
            if not line or line.startswith("#"):
                continue

            parts = line.split("|")
            if len(parts) != _EXPECTED_FIELDS:
                msg = (
                    f"line {line_num}: expected {_EXPECTED_FIELDS} pipe-delimited "
                    f"fields, got {len(parts)}: {line!r}"
                )
                raise ValueError(msg)

            packages.append(
                PackageInfo(
                    name=parts[0],
                    epoch=parts[1],
                    version=parts[2],
                    release=parts[3],
                    arch=parts[4],
                    unknown=parts[5],
                    license=parts[6],
                )
            )

    return packages
