import math
import unittest

from cadipy.errors import InvalidArgumentError
from cadipy.units import deg_to_sw_rad, mm_to_sw_m, sw_m_to_mm, sw_rad_to_deg


class TestUnits(unittest.TestCase):
    def test_length_conversions_use_millimetres_at_public_boundary(self):
        self.assertAlmostEqual(mm_to_sw_m(100.0), 0.1)
        self.assertAlmostEqual(sw_m_to_mm(0.003), 3.0)

    def test_angle_conversions_use_degrees_at_public_boundary(self):
        self.assertAlmostEqual(deg_to_sw_rad(180.0), math.pi)
        self.assertAlmostEqual(sw_rad_to_deg(math.pi / 4), 45.0)

    def test_unit_conversions_reject_non_finite_values(self):
        for value in (math.nan, math.inf, -math.inf):
            with self.assertRaises(InvalidArgumentError):
                mm_to_sw_m(value)


if __name__ == "__main__":
    unittest.main()
