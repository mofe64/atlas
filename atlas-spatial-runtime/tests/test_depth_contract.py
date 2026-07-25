import unittest

import numpy as np

from atlas_spatial_runtime.depth_contract import millimetres_to_metres


class DepthContractTests(unittest.TestCase):
    def test_conversion_is_explicit_and_preserves_invalid_sentinel(self):
        source = np.asarray([[0, 500], [2000, 65535]], dtype=np.uint16)
        converted = millimetres_to_metres(source)
        self.assertTrue(np.isnan(converted[0, 0]))
        self.assertAlmostEqual(float(converted[0, 1]), 0.5)
        self.assertAlmostEqual(float(converted[1, 0]), 2.0)
        self.assertAlmostEqual(float(converted[1, 1]), 65.535, places=3)

    def test_conversion_rejects_non_native_input(self):
        with self.assertRaisesRegex(ValueError, "uint16"):
            millimetres_to_metres(np.ones((2, 2), dtype=np.float32))


if __name__ == "__main__":
    unittest.main()

