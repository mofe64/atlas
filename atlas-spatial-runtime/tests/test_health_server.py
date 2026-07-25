import unittest

from atlas_spatial_runtime.health_server import HealthServer, _read_line


class HealthServerTests(unittest.TestCase):
    def test_requires_absolute_socket_path(self):
        with self.assertRaisesRegex(ValueError, "absolute"):
            HealthServer("spatial.sock", lambda: {})

    def test_reads_one_bounded_request(self):
        class Connection:
            def __init__(self):
                self.chunks = [b'{"protocolVersion":"2",', b'"type":"probe"}\n']

            def recv(self, _size):
                return self.chunks.pop(0)

        self.assertEqual(
            _read_line(Connection(), 4096),
            b'{"protocolVersion":"2","type":"probe"}',
        )


if __name__ == "__main__":
    unittest.main()
