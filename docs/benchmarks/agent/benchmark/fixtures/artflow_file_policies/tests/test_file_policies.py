import unittest

from files.policies import FilePurposePolicy, file_purpose_policy


class TestFilePolicies(unittest.TestCase):
    def test_unknown_purpose_raises(self):
        with self.assertRaises(ValueError):
            file_purpose_policy("not_a_purpose")

    def test_accompaniment_contract(self):
        policy = file_purpose_policy("accompaniment")
        self.assertEqual(policy.max_mb, 100)
        self.assertIn(".flac", policy.extensions)
        self.assertIn("audio/flac", policy.content_types)

    def test_lyrics_limit(self):
        self.assertEqual(file_purpose_policy("lyrics_script").max_mb, 20)

    def test_policy_is_immutable(self):
        policy = file_purpose_policy("other")
        with self.assertRaises(Exception):
            policy.max_mb = 1  # type: ignore[misc]

    def test_returns_frozen_policy_type(self):
        self.assertIsInstance(file_purpose_policy("program_image"), FilePurposePolicy)


if __name__ == "__main__":
    unittest.main()
