import unittest

from offipy import aesthetic


class TestColorTools(unittest.TestCase):
    def test_parse_rgb_formats(self):
        self.assertEqual(aesthetic.parse_rgb("rgb(255, 255, 255)"), (255, 255, 255))
        self.assertEqual(aesthetic.parse_rgb("rgba(34, 81, 255, 0.5)"), (34, 81, 255))
        self.assertEqual(aesthetic.parse_rgb("#2251FF"), (34, 81, 255))
        self.assertIsNone(aesthetic.parse_rgb("transparent"))
        self.assertIsNone(aesthetic.parse_rgb(None))

    def test_parse_rgba_alpha(self):
        self.assertEqual(aesthetic.parse_rgba("rgba(34, 81, 255, 0.5)"), (34, 81, 255, 0.5))
        self.assertEqual(aesthetic.parse_rgba("rgba(0, 0, 0, 0)"), (0, 0, 0, 0.0))
        self.assertEqual(aesthetic.parse_rgba("rgb(34, 81, 255)"), (34, 81, 255, 1.0))
        self.assertEqual(aesthetic.parse_rgba("#2251FF"), (34, 81, 255, 1.0))
        self.assertIsNone(aesthetic.parse_rgba(None))

    def test_parse_rgb_transparent_is_none(self):
        self.assertIsNone(aesthetic.parse_rgb("rgba(0, 0, 0, 0)"))
        self.assertEqual(aesthetic.parse_rgb("rgba(0, 0, 0, 0.5)"), (0, 0, 0))

    def test_composite_blends_semi_transparent(self):
        self.assertEqual(aesthetic._composite((0, 0, 0, 0.5), (255, 255, 255)), (128, 128, 128))
        self.assertIsNone(aesthetic._composite((0, 0, 0, 0.0), (255, 255, 255)))
        self.assertEqual(aesthetic._composite((34, 81, 255, 1.0), (255, 255, 255)), (34, 81, 255))
        self.assertIsNone(aesthetic._composite(None, (255, 255, 255)))

    def test_contrast_ratio_white_black(self):
        self.assertGreater(aesthetic.contrast_ratio((0, 0, 0), (255, 255, 255)), 20.0)
        self.assertEqual(aesthetic.contrast_ratio((255, 255, 255), (255, 255, 255)), 1.0)

    def test_neutral_detection(self):
        self.assertTrue(aesthetic._is_neutral((0, 0, 0)))
        self.assertTrue(aesthetic._is_neutral((128, 128, 128)))
        self.assertFalse(aesthetic._is_neutral((34, 81, 255)))
        self.assertFalse(aesthetic._is_neutral((56, 189, 248)))


if __name__ == "__main__":
    unittest.main()
