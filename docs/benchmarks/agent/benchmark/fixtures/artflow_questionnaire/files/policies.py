"""Single source of truth for uploaded-file limits and format allow-lists."""

from __future__ import annotations

from dataclasses import dataclass

from django.conf import settings

QUESTIONNAIRE_MAX_FILE_MB_HARD_LIMIT = 500

FILE_PURPOSE_LABELS: dict[str, str] = {
    "program_image": "节目图片",
    "public_image": "公开首页图片",
    "showcase_image": "往届风采图片",
    "accompaniment": "伴奏",
    "background_video": "背景视频",
    "performance_video": "演唱视频",
    "lyrics_script": "歌词/台本",
    "host_material": "主持稿素材",
    "other": "其他附件",
}


@dataclass(frozen=True)
class FilePurposePolicy:
    max_mb: int
    extensions: frozenset[str]
    content_types: frozenset[str]
    direct_upload_allowed: bool = True


FILE_PURPOSE_POLICIES: dict[str, FilePurposePolicy] = {
    "program_image": FilePurposePolicy(
        max_mb=10,
        extensions=frozenset({".jpg", ".jpeg", ".png", ".webp"}),
        content_types=frozenset({"image/jpeg", "image/png", "image/webp"}),
    ),
    "public_image": FilePurposePolicy(
        max_mb=10,
        extensions=frozenset({".jpg", ".jpeg", ".png", ".webp"}),
        content_types=frozenset({"image/jpeg", "image/png", "image/webp"}),
    ),
    "showcase_image": FilePurposePolicy(
        max_mb=10,
        extensions=frozenset({".jpg", ".jpeg", ".png", ".webp"}),
        content_types=frozenset({"image/jpeg", "image/png", "image/webp"}),
    ),
    "accompaniment": FilePurposePolicy(
        max_mb=100,
        extensions=frozenset({".mp3", ".wav", ".m4a", ".flac"}),
        content_types=frozenset(
            {"audio/flac", "audio/mp4", "audio/mpeg", "audio/wav", "audio/x-wav"}
        ),
    ),
    "background_video": FilePurposePolicy(
        max_mb=500,
        extensions=frozenset({".mp4", ".mov", ".webm"}),
        content_types=frozenset({"video/mp4", "video/quicktime", "video/webm"}),
    ),
    "performance_video": FilePurposePolicy(
        max_mb=500,
        extensions=frozenset({".mp4", ".mov", ".webm"}),
        content_types=frozenset({"video/mp4", "video/quicktime", "video/webm"}),
    ),
    "lyrics_script": FilePurposePolicy(
        max_mb=20,
        extensions=frozenset({".txt", ".doc", ".docx", ".pdf"}),
        content_types=frozenset(
            {
                "application/msword",
                "application/pdf",
                "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                "text/plain",
            }
        ),
    ),
    "host_material": FilePurposePolicy(
        max_mb=20,
        extensions=frozenset({".txt", ".doc", ".docx", ".pdf"}),
        content_types=frozenset(
            {
                "application/msword",
                "application/pdf",
                "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                "text/plain",
            }
        ),
    ),
    "other": FilePurposePolicy(
        max_mb=50,
        extensions=frozenset({".txt", ".doc", ".docx", ".pdf", ".zip"}),
        content_types=frozenset(
            {
                "application/msword",
                "application/pdf",
                "application/zip",
                "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                "text/plain",
            }
        ),
    ),
}


def file_purpose_policy(purpose: str) -> FilePurposePolicy:
    """Return the immutable policy for a registered purpose."""
    try:
        return FILE_PURPOSE_POLICIES[purpose]
    except KeyError as error:
        raise ValueError(f"Unknown file purpose: {purpose}") from error


def effective_file_policy(purpose: str, activity=None) -> FilePurposePolicy:
    """Resolve the nominal file contract against the activity's runtime cap."""
    policy = file_purpose_policy(purpose)
    if purpose not in {"background_video", "performance_video"} or activity is None:
        return policy
    from core.models import Activity

    if activity.data_lifecycle != Activity.DataLifecycle.FORMAL:
        return policy
    configured_mb = max(0, int(settings.ARTFLOW_VIDEO_UPLOAD_MAX_MB))
    return FilePurposePolicy(
        max_mb=min(policy.max_mb, configured_mb),
        extensions=policy.extensions,
        content_types=policy.content_types,
        direct_upload_allowed=configured_mb > 0,
    )
