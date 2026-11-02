import unittest

from common.private_test_questionnaire import (
    answers_from_reference_row,
    due_rounds_for_reference_row,
    historical_2025_questionnaire,
)


def walk_file_questions(node):
    if isinstance(node, dict):
        if node.get("type") == "file":
            yield node
        for value in node.values():
            yield from walk_file_questions(value)
    elif isinstance(node, list):
        for value in node:
            yield from walk_file_questions(value)


class TestQuestionnaire(unittest.TestCase):
    def test_due_rounds_without_advancement(self):
        self.assertEqual(
            due_rounds_for_reference_row({}), frozenset({"r1", "r2"})
        )

    def test_due_rounds_top10_only(self):
        self.assertEqual(
            due_rounds_for_reference_row({"advanced_top10": "YES"}),
            frozenset({"r1", "r2", "r3"}),
        )

    def test_due_rounds_top5(self):
        self.assertEqual(
            due_rounds_for_reference_row(
                {"advanced_top10": "YES", "advanced_top5": "YES"}
            ),
            frozenset({"r1", "r2", "r3", "r4"}),
        )

    def test_answers_are_mapped_to_question_keys(self):
        row = {
            "round1_group_song": " A ",
            "round2_song": "B",
            "actual_round3_song": "C",
            "r4_guest_song": "D",
            "actual_final_song_raw": "E",
        }
        self.assertEqual(
            answers_from_reference_row(row),
            {
                "r1.song": "A",
                "r2.song": "B",
                "r3.song": "C",
                "r3.guest_name": "D",
                "r4.song": "E",
            },
        )

    def test_blank_answers_are_skipped(self):
        self.assertEqual(answers_from_reference_row({"round1_group_song": "  "}), {})

    def test_file_questions_follow_the_file_policy(self):
        questionnaire = historical_2025_questionnaire()
        file_questions = {
            question["file"]["purpose"]: question
            for question in walk_file_questions(questionnaire)
        }
        self.assertEqual(file_questions["background_video"]["file"]["max_mb"], 500)
        self.assertIn(".mp4", file_questions["background_video"]["file"]["extensions"])
        self.assertEqual(file_questions["accompaniment"]["file"]["max_mb"], 100)


if __name__ == "__main__":
    unittest.main()
