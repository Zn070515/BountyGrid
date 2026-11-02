"""Media delivery seam for authorized file downloads.

``controlled_media`` owns the authorization decision — it resolves which model a
path belongs to and checks the caller against it. This module owns only *how the
bytes leave the process*, so the two concerns can move independently:

* :class:`LocalFilesystemDelivery` streams the file through this worker, which is
  the current behaviour and remains a valid fallback.
* An object-storage backend can later answer with a short-lived signed redirect
  (``302``) so a large download stops occupying a sync Gunicorn worker. Django
  keeps the authorization decision either way — the object store only carries
  bytes, and private buckets stay private.

Only the local backend ships today: media has not moved to object storage, so
configuring any other name is a startup error rather than a silent fallback.
"""

from __future__ import annotations

import os
from typing import Protocol

from django.conf import settings
from django.core.exceptions import ImproperlyConfigured
from django.http import FileResponse, Http404, HttpResponseBase

DELIVERY_BACKEND_LOCAL = "local"


class DeliveryBackend(Protocol):
    """How authorized bytes reach the client."""

    name: str

    def deliver(self, relative_path: str, *, download_name: str | None = None) -> HttpResponseBase:
        """Return the response for an already-authorized relative media path."""
        ...


def _local_file_response(relative_path: str, download_name: str | None = None) -> FileResponse:
    media_root = os.path.realpath(settings.MEDIA_ROOT)
    full_path = os.path.realpath(os.path.join(media_root, relative_path))
    if not full_path.startswith(f"{media_root}{os.sep}"):
        raise Http404("Invalid media path.")
    if not os.path.isfile(full_path):
        raise Http404("Media file not found.")
    # "" and None are equivalent to django.http.FileResponse (both fall back to
    # the file's own name), but only a string satisfies its signature.
    return FileResponse(open(full_path, "rb"), as_attachment=False, filename=download_name or "")


class LocalFilesystemDelivery:
    """Stream ``MEDIA_ROOT`` bytes through this worker.

    Correct but not scale-free: a sync Gunicorn worker is busy for the whole
    download, so a handful of concurrent large files can starve the application.
    That is the reason the delivery step is a seam at all.
    """

    name = DELIVERY_BACKEND_LOCAL

    def deliver(self, relative_path: str, *, download_name: str | None = None) -> HttpResponseBase:
        return _local_file_response(relative_path, download_name)


_BACKENDS: dict[str, DeliveryBackend] = {
    DELIVERY_BACKEND_LOCAL: LocalFilesystemDelivery(),
}


def delivery_backend_names() -> frozenset[str]:
    """The backend names that currently have an implementation."""
    return frozenset(_BACKENDS)


def get_delivery_backend() -> DeliveryBackend:
    """Return the configured backend, refusing a name nothing implements."""
    configured = getattr(settings, "ARTFLOW_DELIVERY_BACKEND", DELIVERY_BACKEND_LOCAL)
    name = str(configured).strip().lower()
    backend = _BACKENDS.get(name)
    if backend is None:
        raise ImproperlyConfigured(
            "ARTFLOW_DELIVERY_BACKEND must be one of "
            f"{sorted(delivery_backend_names())}; got {name!r}."
        )
    return backend
