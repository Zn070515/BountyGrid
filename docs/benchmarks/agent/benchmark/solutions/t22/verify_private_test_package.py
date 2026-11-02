from __future__ import annotations

import argparse
import csv
import hashlib
import json
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any


class PackageValidationError(ValueError):
    """Raised when a private-test package is unsafe or internally inconsistent."""


@dataclass(frozen=True)
class ManifestVerification:
    file_count: int
    checked_paths: tuple[str, ...]


def _safe_relative_path(value: object) -> Path:
    if not isinstance(value, str) or not value.strip():
        raise PackageValidationError("manifest path must be a non-empty string")
    # Manifest paths are portable POSIX-style paths even when the verifier
    # runs on Windows.  Do not rewrite them to backslashes before handing
    # them to pathlib: on Linux that would turn a nested path into one
    # literal filename component.
    path = Path(value.replace("\\", "/"))
    if path.is_absolute() or ".." in path.parts:
        raise PackageValidationError(f"path is outside package root: {value}")
    return path


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def verify_manifest(manifest_path: Path) -> ManifestVerification:
    manifest_path = manifest_path.expanduser().resolve()
    package_root = manifest_path.parent
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise PackageValidationError(f"cannot read manifest: {manifest_path}") from error

    entries = manifest.get("files")
    declared_count = manifest.get("file_count")
    if not isinstance(entries, list) or not isinstance(declared_count, int):
        raise PackageValidationError("manifest must contain integer file_count and files list")
    if declared_count != len(entries):
        raise PackageValidationError("manifest file_count does not match files list")

    checked: list[str] = []
    for entry in entries:
        if not isinstance(entry, dict):
            raise PackageValidationError("manifest file entry must be an object")
        relative = _safe_relative_path(entry.get("path"))
        target = (package_root / relative).resolve()
        try:
            target.relative_to(package_root)
        except ValueError as error:
            raise PackageValidationError(
                f"path is outside package root: {entry.get('path')}"
            ) from error
        if not target.is_file():
            raise PackageValidationError(f"manifest file is missing: {entry.get('path')}")
        expected_bytes = entry.get("bytes")
        expected_hash = entry.get("sha256")
        if not isinstance(expected_bytes, int) or not isinstance(expected_hash, str):
            raise PackageValidationError(f"manifest metadata is invalid: {entry.get('path')}")
        if target.stat().st_size != expected_bytes:
            raise PackageValidationError(f"byte count mismatch: {entry.get('path')}")
        if _sha256(target) != expected_hash:
            raise PackageValidationError(f"sha256 mismatch: {entry.get('path')}")
        checked.append(relative.as_posix())

    listed = set(checked)
    actual = {
        path.relative_to(package_root).as_posix()
        for path in package_root.rglob("*")
        if path.is_file() and path.resolve() != manifest_path
    }
    unlisted = sorted(actual - listed)
    if unlisted:
        raise PackageValidationError(f"file is not listed in manifest: {unlisted[0]}")
    missing = sorted(listed - actual)
    if missing:
        raise PackageValidationError(f"manifest file is missing: {missing[0]}")
    return ManifestVerification(declared_count, tuple(sorted(checked)))


def validate_sanitized_vote_csv(path: Path) -> int:
    forbidden = {
        "ip",
        "ip_address",
        "source_ip",
        "voter_name",
        "voter_real_name",
        "phone",
        "mobile",
        "wechat",
    }
    with path.open("r", newline="", encoding="utf-8-sig") as handle:
        reader = csv.DictReader(handle)
        headers = {str(header or "").strip().lower() for header in reader.fieldnames or []}
        leaked = sorted(headers & forbidden)
        if leaked:
            raise PackageValidationError(
                f"sanitized vote CSV contains identity field: {', '.join(leaked)}"
            )
        rows = list(reader)
    if not rows:
        raise PackageValidationError(f"sanitized vote CSV is empty: {path.name}")
    return len(rows)


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise PackageValidationError(f"cannot read JSON fixture: {path}") from error
    if not isinstance(value, dict):
        raise PackageValidationError(f"JSON fixture must be an object: {path}")
    return value


def _csv_rows(path: Path) -> list[dict[str, str]]:
    with path.open("r", newline="", encoding="utf-8-sig") as handle:
        return list(csv.DictReader(handle))


def _find_package_roots(root: Path) -> tuple[Path, Path]:
    root = root.expanduser().resolve()
    addendum = root / "07_realistic_test_addendum"
    if addendum.is_dir():
        return root, addendum
    if (root / "ADDENDUM_MANIFEST.json").is_file():
        return root.parent, root
    raise PackageValidationError("cannot find 07_realistic_test_addendum package root")


def verify_private_test_package(
    root: Path, *, require_sanitized_votes: bool = True
) -> dict[str, Any]:
    package_root, addendum_root = _find_package_roots(root)
    manifest = verify_manifest(addendum_root / "ADDENDUM_MANIFEST.json")
    fixture = _load_json(package_root / "02_私测夹具" / "artflow_2025_full_reference_fixture.json")

    contestants = fixture.get("contestants")
    judges = fixture.get("judges")
    p1 = fixture.get("p1r2_vote", {})
    p2 = fixture.get("p2r2_vote", {})
    if not isinstance(contestants, list) or len(contestants) != 15:
        raise PackageValidationError("fixture must contain exactly 15 contestants")
    if not isinstance(judges, list) or len(judges) != 5:
        raise PackageValidationError("fixture must contain exactly 5 judges")
    if p1.get("valid_votes") != 190 or p2.get("valid_votes") != 120:
        raise PackageValidationError("fixture vote totals must be 190 and 120")

    media_assignments = _csv_rows(addendum_root / "media_assignments.csv")
    panel_assignments = _csv_rows(addendum_root / "judge_panel_assignments.csv")
    score_rows = _csv_rows(addendum_root / "synthetic_judge_scores.csv")
    ticket_rows = _csv_rows(addendum_root / "synthetic_ticket_inventory.csv")
    if len(media_assignments) != 60:
        raise PackageValidationError("fixture must contain exactly 60 media assignments")
    if len(panel_assignments) != 12:
        raise PackageValidationError("fixture must contain exactly 12 panel assignments")
    if len(score_rows) != 555:
        raise PackageValidationError("fixture must contain exactly 555 synthetic criterion scores")
    if len(ticket_rows) != 280:
        raise PackageValidationError("fixture must contain exactly 280 synthetic tickets")

    vote_counts: dict[str, int] = {}
    for path in sorted(addendum_root.rglob("*sanitized*.csv")):
        vote_counts[path.name] = validate_sanitized_vote_csv(path)
    if require_sanitized_votes and vote_counts != {
        "p1r2_ballots_sanitized.csv": 190,
        "p2r2_ballots_sanitized.csv": 120,
    }:
        raise PackageValidationError(f"sanitized vote counts mismatch: {vote_counts}")

    return {
        "manifest_file_count": manifest.file_count,
        "contestants": len(contestants),
        "judges": len(judges),
        "panel_assignments": len(panel_assignments),
        "score_rows": len(score_rows),
        "ticket_rows": len(ticket_rows),
        "media_assignments": len(media_assignments),
        "sanitized_votes": vote_counts,
        "provenance": "SYNTHETIC_TEST_ONLY",
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Verify an ArtFlow private-test package.")
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--json-report", type=Path)
    parser.add_argument("--require-sanitized-votes", action="store_true")
    args = parser.parse_args(argv)
    try:
        report = verify_private_test_package(
            args.root, require_sanitized_votes=args.require_sanitized_votes
        )
    except PackageValidationError as error:
        print(f"PRIVATE_TEST_PACKAGE_INVALID: {error}", file=sys.stderr)
        return 2
    serialized = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True)
    print(serialized)
    if args.json_report:
        args.json_report.parent.mkdir(parents=True, exist_ok=True)
        args.json_report.write_text(serialized + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
