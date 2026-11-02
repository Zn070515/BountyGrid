import hashlib
import json
import tempfile
import unittest
from pathlib import Path

from verify_private_test_package import (
    PackageValidationError,
    validate_sanitized_vote_csv,
    verify_manifest,
)


class TestPrivatePackage(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.file = self.root / "data.txt"
        self.file.write_text("hello", encoding="utf-8")
        self.manifest = self.root / "manifest.json"

    def tearDown(self):
        self.tmp.cleanup()

    def write_manifest(self, entries, file_count=None):
        payload = {
            "file_count": len(entries) if file_count is None else file_count,
            "files": entries,
        }
        self.manifest.write_text(json.dumps(payload), encoding="utf-8")

    def entry(self, path, data):
        return {
            "path": path,
            "bytes": len(data),
            "sha256": hashlib.sha256(data).hexdigest(),
        }

    def test_valid_manifest_passes(self):
        self.write_manifest([self.entry("data.txt", b"hello")])
        result = verify_manifest(self.manifest)
        self.assertEqual(result.file_count, 1)
        self.assertEqual(result.checked_paths, ("data.txt",))

    def test_unlisted_file_is_rejected(self):
        self.write_manifest([self.entry("data.txt", b"hello")])
        (self.root / "extra.txt").write_text("x", encoding="utf-8")
        with self.assertRaises(PackageValidationError):
            verify_manifest(self.manifest)

    def test_wrong_hash_is_rejected(self):
        self.write_manifest([{"path": "data.txt", "bytes": 5, "sha256": "0" * 64}])
        with self.assertRaises(PackageValidationError):
            verify_manifest(self.manifest)

    def test_wrong_file_count_is_rejected(self):
        self.write_manifest([self.entry("data.txt", b"hello")], file_count=2)
        with self.assertRaises(PackageValidationError):
            verify_manifest(self.manifest)

    def test_sanitized_csv_rejects_identity_fields(self):
        votes = self.root / "votes.csv"
        votes.write_text("ballot_id,phone\n1,13800000000\n", encoding="utf-8")
        with self.assertRaises(PackageValidationError):
            validate_sanitized_vote_csv(votes)

    def test_sanitized_csv_rejects_an_empty_file(self):
        votes = self.root / "votes.csv"
        votes.write_text("ballot_id\n", encoding="utf-8")
        with self.assertRaises(PackageValidationError):
            validate_sanitized_vote_csv(votes)

    def test_sanitized_csv_accepts_safe_rows(self):
        votes = self.root / "votes.csv"
        votes.write_text("ballot_id,score\n1,9\n", encoding="utf-8")
        self.assertEqual(validate_sanitized_vote_csv(votes), 1)


if __name__ == "__main__":
    unittest.main()
