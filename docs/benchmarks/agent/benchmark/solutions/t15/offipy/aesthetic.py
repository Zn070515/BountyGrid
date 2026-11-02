"""审美审计：把设计铁律量化成可打分的检查（读 measurement JSON）。

数据源是 HTML→PPTX 转换管线产出的 `<out>_audit/_cache/measurements.json`
（每页 records 带 DOM 坐标 rect、文本 runs 的 fontSize/color、slide 背景色）。
不依赖 Office，纯 Python 可测。

检查维度（铁律来源 docs/ppt_design_research.md §2）：
- whitespace  留白：内容覆盖面积 vs 页面积，留白应 ≥ 40%（一页一观点）
- type-scale  字号层级：一页 ≤ 4 种字号，层级靠字号不靠字体
- palette     色数：非中性色 ≤ 3（60-30-10，一页最多一个强调色）
- contrast    对比度：正文 ≥ 4.5:1，大字 ≥ 3:1（WCAG）
- consistency 一致性：全 deck 标题字号 / 背景 / 间距 8pt 网格是否漂移

每个 finding 带 severity（HIGH/MID/LOW）与页面定位。报告可序列化为 JSON，
也可渲染成 Markdown 供 Claude 迭代。
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from . import design

# 检查维度常量（feedback 学习按维度记权重）
WHITESPACE = "whitespace"
TYPE_SCALE = "type-scale"
PALETTE = "palette"
CONTRAST = "contrast"
CONSISTENCY = "consistency"
ALL_DIMENSIONS = (WHITESPACE, TYPE_SCALE, PALETTE, CONTRAST, CONSISTENCY)

# 页面尺寸（16:9 画布，measure 固定视口）
_CANVAS_W = 1920.0
_CANVAS_H = 1080.0

_SEVERITY_PENALTY = {"HIGH": 25, "MID": 12, "LOW": 5}


# ---------------------------------------------------------------- 颜色工具


def parse_rgba(color: str | None) -> tuple[int, int, int, float] | None:
    """'rgb(34, 81, 255)' / 'rgba(...)' / '#2251FF' → (r, g, b, alpha)。

    rgb(...)/hex 视为不透明（alpha=1.0）；rgba 读 alpha（0..1）。只做解析，
    透明如何处理（忽略/回退/合成）由调用方决定。
    """
    if not color:
        return None
    color = color.strip()
    m = re.fullmatch(r"rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*([\d.]+)\s*)?\)", color)
    if m:
        alpha = float(m.group(4)) if m.group(4) is not None else 1.0
        return (int(m.group(1)), int(m.group(2)), int(m.group(3)), alpha)
    m = re.fullmatch(r"#([0-9a-fA-F]{6})", color)
    if m:
        v = m.group(1)
        return (int(v[0:2], 16), int(v[2:4], 16), int(v[4:6], 16), 1.0)
    return None


def parse_rgb(color: str | None) -> tuple[int, int, int] | None:
    """'rgb(34, 81, 255)' / '#2251FF' → (r, g, b)；rgba 全透明(alpha≤0) → None。

    透明即「无颜色」：让调用方 `parse_rgb(x) or 兜底色` 自然回退到页面背景，
    避免透明黑被当不透明黑造成对比度假阳性。
    """
    rgba = parse_rgba(color)
    if rgba is None or rgba[3] <= 0:
        return None
    return rgba[:3]


def _blend(
    fg: tuple[int, int, int], alpha: float, bg: tuple[int, int, int]
) -> tuple[int, int, int]:
    """source-over：前景色按 alpha 与底色合成（用于半透明颜色）。"""
    return (
        round(fg[0] * alpha + bg[0] * (1 - alpha)),
        round(fg[1] * alpha + bg[1] * (1 - alpha)),
        round(fg[2] * alpha + bg[2] * (1 - alpha)),
    )


def _composite(
    rgba: tuple[int, int, int, float] | None, base: tuple[int, int, int]
) -> tuple[int, int, int] | None:
    """rgba 与底色合成；解析失败/全透明 → None（无颜色，交由调用方兜底）。"""
    if rgba is None:
        return None
    r, g, b, alpha = rgba
    if alpha <= 0:
        return None
    if alpha >= 1:
        return (r, g, b)
    return _blend((r, g, b), alpha, base)


def _channel_luminance(c: float) -> float:
    c = c / 255.0
    return c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4


def relative_luminance(rgb: tuple[int, int, int]) -> float:
    r, g, b = (_channel_luminance(c) for c in rgb)
    return 0.2126 * r + 0.7152 * g + 0.0722 * b


def contrast_ratio(fg: tuple[int, int, int], bg: tuple[int, int, int]) -> float:
    l1, l2 = relative_luminance(fg), relative_luminance(bg)
    if l1 < l2:
        l1, l2 = l2, l1
    return (l1 + 0.05) / (l2 + 0.05)


def _saturation(rgb: tuple[int, int, int]) -> float:
    """饱和度近似（0-1）。< 0.12 视为中性色（黑/白/灰/近灰）。

    用 HSL 饱和度 + 亮度下限：除以 max 通道会高估暗色的饱和度
    （rgb(10,10,14) 判成 0.29，其实近黑），极暗色感知为黑应算中性。
    """
    r, g, b = (c / 255.0 for c in rgb)
    mx, mn = max(r, g, b), min(r, g, b)
    lightness = (mx + mn) / 2.0
    if mx == mn or lightness < 0.10:
        return 0.0
    if lightness <= 0.5:
        return (mx - mn) / (mx + mn)
    return (mx - mn) / (2.0 - mx - mn)


def _is_neutral(rgb: tuple[int, int, int]) -> bool:
    return _saturation(rgb) < 0.12


# ---------------------------------------------------------------- 数据结构


@dataclass
class Finding:
    dimension: str
    severity: str  # HIGH / MID / LOW
    page: int
    message: str
    detail: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        return {
            "dimension": self.dimension,
            "severity": self.severity,
            "page": self.page,
            "message": self.message,
            **({"detail": self.detail} if self.detail else {}),
        }


@dataclass
class PageScore:
    index: int
    score: int
    findings: list[Finding] = field(default_factory=list)
    metrics: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        return {
            "index": self.index,
            "score": self.score,
            "metrics": self.metrics,
            "findings": [f.to_dict() for f in self.findings],
        }


@dataclass
class AestheticReport:
    total_score: int
    pages: list[PageScore] = field(default_factory=list)
    theme: str | None = None
    warnings: list[str] = field(default_factory=list)
    deck_findings: list[Finding] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "total_score": self.total_score,
            "theme": self.theme,
            "warnings": self.warnings,
            "deck_findings": [f.to_dict() for f in self.deck_findings],
            "pages": [p.to_dict() for p in self.pages],
        }

    def markdown(self) -> str:
        lines = ["# 审美审计报告", ""]
        if self.theme:
            lines.append(f"主题：`{self.theme}`")
        lines.append(f"总分：**{self.total_score} / 100**")
        lines.append("")
        if self.deck_findings:
            lines.append("## 全 deck 一致性")
            lines.extend(
                f"- `[{f.severity}]` {f.dimension}：{f.message}" for f in self.deck_findings
            )
            lines.append("")
        for p in self.pages:
            lines.append(f"## 第 {p.index} 页 · {p.score} 分")
            if p.metrics:
                parts = " · ".join(f"{k}={v}" for k, v in p.metrics.items())
                lines.append(f"> {parts}")
            if not p.findings:
                lines.append("_无问题_")
            lines.extend(f"- `[{f.severity}]` {f.dimension}：{f.message}" for f in p.findings)
            lines.append("")
        return "\n".join(lines)


# ---------------------------------------------------------------- 单页审计


def _num(value: Any, default: float = 0.0) -> float:
    """安全转 float：非法/NaN 值回退 default，不让畸形 measurement 崩整个审计。"""
    try:
        f = float(value)
    except (TypeError, ValueError):
        return default
    return default if f != f else f  # NaN 自不等


def _records(slide: dict[str, Any]) -> list[dict[str, Any]]:
    recs = slide.get("records")
    return recs if isinstance(recs, list) else []


def _rec_runs(rec: dict[str, Any]) -> list[dict[str, Any]]:
    runs = rec.get("runs")
    return runs if isinstance(runs, list) else []


def _collect_text_runs(records: list[dict[str, Any]]) -> list[dict[str, Any]]:
    runs: list[dict[str, Any]] = []
    for rec in records:
        if rec.get("kind") != "text":
            continue
        rec_runs = rec.get("runs")
        for run in rec_runs if isinstance(rec_runs, list) else []:
            if run.get("linebreak"):
                continue
            if (run.get("text") or "").strip():
                runs.append(run)
    return runs


def _rect_area(rec: dict[str, Any]) -> float:
    r = rec.get("rect") or {}
    return max(0.0, _num(r.get("w"))) * max(0.0, _num(r.get("h")))


def _cluster_font_sizes(sizes: list[float], tolerance: float = 2.0) -> int:
    """字号聚类：排序后相邻差 ≤ tolerance 归一组，返回组数。"""
    if not sizes:
        return 0
    ordered = sorted(sizes)
    groups = 1
    prev = ordered[0]
    for s in ordered[1:]:
        if s - prev > tolerance:
            groups += 1
        prev = s
    return groups


def _audit_whitespace(
    records: list[dict[str, Any]], page_index: int, background: str | None = None
) -> tuple[list[Finding], float]:
    page_area = _CANVAS_W * _CANVAS_H
    bg = parse_rgb(background)
    # 整页背景形状（≥90% 页面积的非文本 record）是画布本身，不算内容覆盖。
    # 只有当它匹配页面背景（或没有 deco 信息）才算背景；全出血 hero 图/图表有
    # 独立 deco.bg、不匹配页面背景，仍计入内容，避免真实拥挤被误放行。
    content_area = 0.0
    for r in records:
        area = _rect_area(r)
        if r.get("kind") != "text" and area / page_area >= 0.90:
            deco = r.get("deco")
            deco_bg = parse_rgb(deco.get("bg")) if isinstance(deco, dict) else None
            # 仅当 deco 色明确存在且 ≠ 页面背景（真·全出血 hero 图/图表）才算内容；
            # 背景 shape（deco 无/未知/匹配）一律排除
            if not (deco_bg is not None and bg is not None and deco_bg != bg):
                continue
        content_area += area
    ratio = min(1.0, content_area / page_area)
    findings = []
    if ratio > 0.75:
        sev = "HIGH"
        msg = f"内容覆盖 {ratio:.0%}，页面过满，留白不足 25%"
    elif ratio > 0.60:
        sev = "MID"
        msg = f"内容覆盖 {ratio:.0%}，留白 {1 - ratio:.0%}（目标 ≥ 40%），建议拆页或减元素"
    else:
        return [], ratio
    findings.append(Finding(WHITESPACE, sev, page_index, msg, {"content_ratio": round(ratio, 3)}))
    return findings, ratio


def _audit_type_scale(runs: list[dict[str, Any]], page_index: int) -> tuple[list[Finding], int]:
    sizes = [_num(r.get("fontSize")) for r in runs if _num(r.get("fontSize")) > 0]
    n = _cluster_font_sizes(sizes)
    findings = []
    if n > 6:
        sev, threshold = "HIGH", "6"
    elif n > 4:
        sev, threshold = "MID", "4"
    else:
        return [], n
    findings.append(
        Finding(
            TYPE_SCALE,
            sev,
            page_index,
            f"出现 {n} 种字号（一页应 ≤ {threshold} 种）",
            {"font_size_clusters": n},
        )
    )
    return findings, n


def _audit_palette(
    records: list[dict[str, Any]], background: str | None, page_index: int
) -> tuple[list[Finding], int]:
    """非中性色计数（背景 + 文本 + 形状/装饰填充色）。"""
    colors: set[tuple[int, int, int]] = set()
    bg = parse_rgb(background)
    if bg:
        colors.add(bg)
    for rec in records:
        kind = rec.get("kind")
        if kind == "text":
            rec_runs = rec.get("runs")
            for run in rec_runs if isinstance(rec_runs, list) else []:
                c = parse_rgb(run.get("color"))
                if c:
                    colors.add(c)
        else:
            deco = rec.get("deco")
            if not isinstance(deco, dict):
                deco = {}
            c = parse_rgb(deco.get("bg"))
            if c:
                colors.add(c)
    non_neutral = {c for c in colors if not _is_neutral(c)}
    n = len(non_neutral)
    findings = []
    if n > 5:
        sev, threshold = "HIGH", "3"
    elif n > 3:
        sev, threshold = "MID", "3"
    else:
        return [], n
    findings.append(
        Finding(
            PALETTE,
            sev,
            page_index,
            f"出现 {n} 种非中性色（60-30-10：一页应 ≤ {threshold}，仅一个强调色）",
            {"non_neutral_colors": n},
        )
    )
    return findings, n


def _audit_contrast(
    records: list[dict[str, Any]], background: str | None, page_index: int
) -> list[Finding]:
    bg = parse_rgb(background) or (255, 255, 255)
    findings = []
    # 同一前景色在不同背景（页面 vs 色卡）上对比度不同，seen 需带背景键
    seen: set[tuple[tuple[int, int, int], tuple[int, int, int]]] = set()
    for rec in records:
        if rec.get("kind") != "text":
            continue
        # 文本所在形状若有填充背景（色卡/卡片），用它作对比基准，而非整页背景；
        # 半透明背景/前景按底色调 alpha 合成后再算对比度（透明≠不透明黑）
        deco = rec.get("deco")
        if not isinstance(deco, dict):
            deco = {}
        rec_bg = _composite(parse_rgba(deco.get("bg")), bg) or bg
        rec_runs = rec.get("runs")
        for run in rec_runs if isinstance(rec_runs, list) else []:
            fg = _composite(parse_rgba(run.get("color")), rec_bg)
            if not fg or (fg, rec_bg) in seen:
                continue
            seen.add((fg, rec_bg))
            size = _num(run.get("fontSize"))
            ratio = contrast_ratio(fg, rec_bg)
            threshold = 3.0 if size >= 24 else 4.5
            if ratio >= threshold:
                continue
            sev = "HIGH" if ratio < 3.0 else "MID"
            findings.append(
                Finding(
                    CONTRAST,
                    sev,
                    page_index,
                    f"对比度 {ratio:.1f}:1 < {threshold:g}:1（{size:.0f}px 文本 vs 背景）",
                    {"ratio": round(ratio, 2), "size": int(size)},
                )
            )
    return findings


# ---------------------------------------------------------------- 一致性（task #43）


def _consistency_findings(measurement: dict[str, Any], theme: str | None) -> list[Finding]:
    slides = measurement.get("slides") or []
    if len(slides) < 2:
        return []
    findings = []

    # 1) 标题字号漂移：取每页最大字号，跨页极差 > 6px 且页数 ≥ 3 才报（2 页布局本就不同）。
    #    装饰性大数字（>120px，如 big-number 布局的 200px）不算标题，排除以免误报
    max_sizes: list[tuple[int, float]] = []
    for i, s in enumerate(slides, start=1):
        sizes = [
            _num(r.get("fontSize"))
            for rec in _records(s)
            if rec.get("kind") == "text"
            for r in _rec_runs(rec)
            if _num(r.get("fontSize")) > 0 and _num(r.get("fontSize")) <= 120
        ]
        if sizes:
            max_sizes.append((i, max(sizes)))
    if len(max_sizes) >= 3:
        vals = [v for _, v in max_sizes]
        spread = max(vals) - min(vals)
        if spread > 6:
            findings.append(
                Finding(
                    CONSISTENCY,
                    "MID",
                    0,
                    f"标题字号跨页漂移 {spread:.0f}px"
                    f"（各页最大字号 {[int(v) for _, v in max_sizes]}）",
                    {"spread": round(spread, 1)},
                )
            )

    # 2) 背景漂移：主题给定时用 token 的主/变体背景作允许集（theme 参数真正生效）；
    #    未给时允许 2 种（主背景 + 1 反色变体）
    bgs = []
    for s in slides:
        c = parse_rgb((s.get("slide") or {}).get("background"))
        if c:
            bgs.append(c)
    actual = set(bgs)
    if theme and theme in design.THEMES:
        t = design.THEMES[theme]
        for vars_ in (t.base_vars, t.variant_vars):
            c = parse_rgb(vars_.get("--bg"))
            if c:
                actual.discard(c)
    limit = 1 if theme else 2
    if len(bgs) >= 3 and len(actual) > limit:
        hexes = [f"#{r:02X}{g:02X}{b:02X}" for r, g, b in sorted(actual)]
        msg = (
            f"全 deck 出现非主题背景色：{', '.join(hexes)}"
            if theme
            else f"全 deck 出现 {len(set(bgs))} 种背景色（最多主背景 + 1 变体）：{', '.join(hexes)}"
        )
        findings.append(Finding(CONSISTENCY, "MID", 0, msg, {"backgrounds": hexes}))

    # 3) 8pt 网格：间距/尺寸对齐 8px 的比例。规则密集的卡片 deck 允许部分不对齐，
    #    不对齐比例 > 30% 才报（避免小元素 sub-pixel 误报）
    off = 0
    total = 0
    for s in slides:
        for rec in s.get("records") or []:
            r = rec.get("rect") or {}
            for key in ("x", "y", "w", "h"):
                v = r.get(key)
                if v is None:
                    continue
                total += 1
                if abs(_num(v) % design.GRID_UNIT) > 0.5:
                    off += 1
    if total > 0 and off / total > 0.30:
        findings.append(
            Finding(
                CONSISTENCY,
                "LOW",
                0,
                f"{off}/{total}（{off / total:.0%}）元素坐标未对齐 8px 网格",
                {"off_grid_ratio": round(off / total, 2)},
            )
        )

    return findings


# ---------------------------------------------------------------- 评分


def _score_pages(
    measurement: dict[str, Any], _theme: str | None, weights: dict[str, float] | None = None
) -> list[PageScore]:
    weights = weights or {}
    pages = []
    for i, s in enumerate(measurement.get("slides") or [], start=1):
        records = _records(s)
        runs = _collect_text_runs(records)
        slide_info = s.get("slide") or {}
        background = slide_info.get("background")

        findings: list[Finding] = []
        w_f, content_ratio = _audit_whitespace(records, i, background)
        findings += w_f
        t_f, n_sizes = _audit_type_scale(runs, i)
        findings += t_f
        p_f, n_colors = _audit_palette(records, background, i)
        findings += p_f
        findings += _audit_contrast(records, background, i)

        # 反色页（.dark/.light）背景与默认背景不同，允许——主题未给时不误报
        score = 100
        for f in findings:
            w = weights.get(f.dimension, 1.0)
            score -= int(_SEVERITY_PENALTY[f.severity] * w)
        score = max(0, score)

        pages.append(
            PageScore(
                index=i,
                score=score,
                findings=findings,
                metrics={
                    "content_ratio": round(content_ratio, 2),
                    "font_clusters": n_sizes,
                    "non_neutral_colors": n_colors,
                },
            )
        )
    return pages


# ---------------------------------------------------------------- 入口


def load_measurement(path: str | Path) -> dict[str, Any]:
    """读 measurement JSON（HTML→PPTX 管线产物）。"""
    p = Path(path)
    if not p.exists():
        raise FileNotFoundError(p)
    data = json.loads(p.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or not isinstance(data.get("slides"), list):
        raise ValueError(f"{p} 不是合法 measurement（缺 'slides' 数组）")  # noqa: TRY004 — JSON 数据校验是「数据形状错误」用 ValueError；TypeError 留给 Python 参数类型契约
    return data


def audit_measurement(
    measurement: dict[str, Any],
    theme: str | None = None,
    weights: dict[str, float] | None = None,
) -> AestheticReport:
    """对 measurement 数据做审美审计，返回报告。"""
    weights = weights or {}
    pages = _score_pages(measurement, theme, weights)
    consistency = _consistency_findings(measurement, theme)
    # 跨页一致性 finding 是 deck 级（page=0），单独暴露；计分按均摊扣。
    # 一致性维度同样受反馈权重调节，否则 feedback 的 consistency 记录永远无效
    total = sum(p.score for p in pages) / len(pages) if pages else 0
    consistency_penalty = (
        sum(int(_SEVERITY_PENALTY[f.severity] * weights.get(CONSISTENCY, 1.0)) for f in consistency)
        if pages
        else 0
    )
    total = max(0, int(total - consistency_penalty / max(1, len(pages))))
    return AestheticReport(
        total_score=total,
        pages=pages,
        theme=theme,
        warnings=[],
        deck_findings=consistency,
    )


def audit(
    html_path: str | Path,
    measurement_path: str | Path | None = None,
    theme: str | None = None,
    weights: dict[str, float] | None = None,
) -> AestheticReport:
    """对一份 deck 做审美审计。

    measurement_path 缺省时自动找 `<html_stem>_audit/_cache/measurements.json`
    （转换管线产物）。找不到则只返回基于 HTML 侧 token 检查的报告。
    """
    html_path = Path(html_path)
    if measurement_path is None:
        candidate = html_path.parent / f"{html_path.stem}_audit" / "_cache" / "measurements.json"
        if candidate.exists():
            measurement_path = candidate
    if measurement_path is not None:
        return audit_measurement(load_measurement(measurement_path), theme=theme, weights=weights)
    return AestheticReport(
        total_score=0,
        theme=theme,
        warnings=[
            f"找不到 measurement（预期在 {html_path.parent / (html_path.stem + '_audit')}），"
            "先跑 deck.render 再审计"
        ],
    )
