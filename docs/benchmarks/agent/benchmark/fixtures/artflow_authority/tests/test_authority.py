import threading
import unittest

from common.authority import (
    RULESET_FREEZE,
    STAGE_RESULT_CONFIRM,
    authority_authorized,
    authority_write,
    parse_bulk_create_options,
)


class TestAuthority(unittest.TestCase):
    def test_outside_a_write_is_unauthorized(self):
        self.assertFalse(authority_authorized(RULESET_FREEZE))

    def test_grant_and_restore(self):
        with authority_write(RULESET_FREEZE):
            self.assertTrue(authority_authorized(RULESET_FREEZE))
        self.assertFalse(authority_authorized(RULESET_FREEZE))

    def test_nested_grant_restores_the_outer_scope(self):
        with authority_write(RULESET_FREEZE):
            with authority_write(STAGE_RESULT_CONFIRM):
                self.assertTrue(authority_authorized(RULESET_FREEZE))
                self.assertTrue(authority_authorized(STAGE_RESULT_CONFIRM))
            self.assertTrue(authority_authorized(RULESET_FREEZE))
            self.assertFalse(authority_authorized(STAGE_RESULT_CONFIRM))

    def test_authority_does_not_leak_between_threads(self):
        seen = []
        with authority_write(RULESET_FREEZE):
            worker = threading.Thread(
                target=lambda: seen.append(authority_authorized(RULESET_FREEZE))
            )
            worker.start()
            worker.join()
        self.assertEqual(seen, [False])

    def test_parse_bulk_create_options_positional(self):
        options = parse_bulk_create_options((None, None, True, "name", ["id"]), {})
        self.assertTrue(options.update_conflicts)
        self.assertEqual(options.update_fields, ("name",))
        self.assertEqual(options.unique_fields, ("id",))

    def test_parse_bulk_create_options_keyword(self):
        options = parse_bulk_create_options(
            (), {"update_conflicts": True, "update_fields": "name", "unique_fields": ["id"]}
        )
        self.assertTrue(options.update_conflicts)
        self.assertEqual(options.update_fields, ("name",))
        self.assertEqual(options.unique_fields, ("id",))


if __name__ == "__main__":
    unittest.main()
