import tempfile
import unittest
from pathlib import Path

from django.conf import settings

if not settings.configured:
    settings.configure(MEDIA_ROOT=tempfile.gettempdir(), ARTFLOW_DELIVERY_BACKEND="local")

from django.core.exceptions import ImproperlyConfigured
from django.http import FileResponse, Http404

from common.delivery import (
    _local_file_response,
    delivery_backend_names,
    get_delivery_backend,
)


class TestDelivery(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(ignore_cleanup_errors=True)
        self.media = Path(self.tmp.name) / "media"
        self.media.mkdir()
        (self.media / "ok.txt").write_text("hello", encoding="utf-8")
        sibling = Path(self.tmp.name) / "media_evil"
        sibling.mkdir()
        (sibling / "secret.txt").write_text("secret", encoding="utf-8")
        self.old_root = settings.MEDIA_ROOT
        self.old_backend = getattr(settings, "ARTFLOW_DELIVERY_BACKEND", "local")
        settings.MEDIA_ROOT = str(self.media)
        settings.ARTFLOW_DELIVERY_BACKEND = "local"

    def tearDown(self):
        settings.MEDIA_ROOT = self.old_root
        settings.ARTFLOW_DELIVERY_BACKEND = self.old_backend
        self.tmp.cleanup()

    def test_local_backend_is_registered(self):
        self.assertIn("local", delivery_backend_names())
        self.assertEqual(get_delivery_backend().name, "local")

    def test_unknown_backend_is_refused(self):
        settings.ARTFLOW_DELIVERY_BACKEND = "s3"
        with self.assertRaises(ImproperlyConfigured):
            get_delivery_backend()

    def test_serves_an_existing_file(self):
        response = _local_file_response("ok.txt")
        try:
            self.assertIsInstance(response, FileResponse)
            self.assertEqual(response.status_code, 200)
        finally:
            response.close()

    def test_rejects_a_sibling_directory_that_shares_the_prefix(self):
        with self.assertRaises(Http404):
            _local_file_response("../media_evil/secret.txt")

    def test_missing_file_is_a_404(self):
        with self.assertRaises(Http404):
            _local_file_response("missing.txt")


if __name__ == "__main__":
    unittest.main()
