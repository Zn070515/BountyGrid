"""设计系统：token 模型 + 内置风格主题 + 主题 CSS 注入。

Claude 写 16:9 deck HTML 时用约定好的 CSS 变量（token）表达设计意图
（对齐 examples/decks/starter/deck.html 的 :root 命名），render 时选择一套
主题注入实际值。这样「内容 HTML」与「视觉主题」解耦：同一份 HTML 换主题即换皮。

token 命名（Claude 直接 var() 引用）：
  色彩角色   --bg / --surface / --ink / --muted / --accent / --accent-soft / --divider
  字体       --font / --font-serif / --font-sans
  字号刻度   --kicker / --title / --body / --caption
  间距       --pad / --gap / --radius / --line-height / --max-line

铁律来源：docs/ppt_design_research.md §2（60-30-10 / 8pt 网格 / 一页 ≤4 种字号 /
对比度 ≥4.5:1）与 §3（麦肯锡 hex / 字体）。
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field


@dataclass(frozen=True)
class Theme:
    """一套完整主题 = 一组 CSS 变量覆盖。

    base_vars 决定默认背景色系；variant_* 提供反色页（章节/封面与内容页混排）的覆盖。
    """

    name: str
    title: str
    description: str
    base_vars: dict[str, str]
    variant_selector: str | None = None
    variant_vars: dict[str, str] = field(default_factory=dict)


# 字号 / 间距 / 行高是所有主题共享的公共刻度（16:9 全屏投影最小可读基线）
_BASE_TOKENS = {
    "--kicker": "20px",  # 眉标
    "--title": "52px",  # 大标题
    "--body": "24px",  # 正文（≥24px，1080p 全屏可读）
    "--caption": "18px",  # 注释/来源
    "--col-title": "34px",  # 两栏/卡片区块标题
    "--num": "30px",  # 卡片序号
    "--display": "200px",  # 大数字页主数字
    "--quote": "52px",  # 引言正文
    "--quote-mark": "72px",  # 引言引号装饰
    "--tl-date": "26px",  # 时间线日期
    "--pad": "96px",  # 页边距（≈5% 安全区）
    "--gap": "24px",  # 组件间距（8pt 网格 × 3）
    "--radius": "8px",
    "--line-height": "1.6",
    "--max-line": "60ch",
    "--letter-spacing": "0.01em",
}

# 主题必须完整提供的颜色 / 字体键
_REQUIRED_KEYS = (
    "--bg",
    "--surface",
    "--ink",
    "--muted",
    "--accent",
    "--accent-soft",
    "--divider",
    "--font",
    "--font-serif",
    "--font-sans",
)

_GEORGIA = '"Georgia", "Times New Roman", serif'
_ARIAL = '"Arial", "Helvetica Neue", sans-serif'
_SEGOE = '"Segoe UI", "Microsoft YaHei", "PingFang SC", Arial, sans-serif'

THEMES: dict[str, Theme] = {
    # ---------------------------------------------------------------- 麦肯锡蓝
    # 电光蓝 #2251FF 克制使用，只高亮关键数据；深藏青 #051C2C 做章节页
    "mckinsey": Theme(
        name="mckinsey",
        title="麦肯锡蓝",
        description=(
            "咨询风：白底 + 深藏青章节页，电光蓝只高亮关键数据。"
            "衬线标题（Georgia）+ 无衬线正文（Arial）。克制是风格核心。"
        ),
        base_vars={
            "--bg": "#FFFFFF",
            "--surface": "#F2F4F7",
            "--ink": "#222222",
            "--muted": "#667085",  # 白底对比 ≈5:1（正文 ≥4.5:1 达标）
            "--accent": "#2251FF",
            "--accent-soft": "#E9EDFF",
            "--divider": "#E4E8EC",
            "--font": _GEORGIA,
            "--font-serif": _GEORGIA,
            "--font-sans": _ARIAL,
            "--radius": "6px",
        },
        variant_selector=".slide.dark",
        variant_vars={
            "--bg": "#051C2C",
            "--surface": "#0C1C23",
            "--ink": "#FFFFFF",
            "--muted": "#A2AAAD",
            "--accent": "#5B8CFF",  # 深底上的电光蓝提亮，保对比度
            "--accent-soft": "#12304A",
            "--divider": "#1B3A4E",
        },
    ),
    # ---------------------------------------------------------------- 学术极简
    "academic": Theme(
        name="academic",
        title="学术极简",
        description=(
            "米白底 + 深蓝，衬线标题、大量留白、直角克制。适合论文汇报 / 方法论 / 严谨内容。"
        ),
        base_vars={
            "--bg": "#FAFAF7",
            "--surface": "#FFFFFF",
            "--ink": "#1A1A1A",
            "--muted": "#6B7280",
            "--accent": "#1F3A5F",
            "--accent-soft": "#E8EEF5",
            "--divider": "#E5E5E0",
            "--font": _GEORGIA,
            "--font-serif": _GEORGIA,
            "--font-sans": _ARIAL,
            "--radius": "2px",
        },
        variant_selector=".slide.dark",
        variant_vars={
            "--bg": "#0F1B2D",
            "--surface": "#16263C",
            "--ink": "#F5F5F2",
            "--muted": "#9AA3AF",
            "--accent": "#8AB4F8",
            "--accent-soft": "#1C2F47",
            "--divider": "#223650",
        },
    ),
    # ---------------------------------------------------------------- 深色科技
    "dark-tech": Theme(
        name="dark-tech",
        title="深色科技",
        description=(
            "藏青底 + 亮青强调，圆角卡片。默认深色，浅色页做内容/收尾。"
            "适合产品发布 / 技术分享 / 数据大屏。"
        ),
        base_vars={
            "--bg": "#0F172A",
            "--surface": "#1E293B",
            "--ink": "#F8FAFC",
            "--muted": "#94A3B8",
            "--accent": "#38BDF8",
            "--accent-soft": "#0B1B33",
            "--divider": "#1E293B",
            "--font": _SEGOE,
            "--font-serif": _GEORGIA,
            "--font-sans": _SEGOE,
            "--radius": "12px",
        },
        variant_selector=".slide.light",
        variant_vars={
            "--bg": "#F8FAFC",
            "--surface": "#FFFFFF",
            "--ink": "#0F172A",
            "--muted": "#64748B",
            "--accent": "#0284C7",
            "--accent-soft": "#E0F2FE",
            "--divider": "#E2E8F0",
        },
    ),
}


def themes() -> list[dict[str, str]]:
    """所有内置主题的元数据清单（给自动选型 / 文档用）。"""
    return [
        {
            "name": t.name,
            "title": t.title,
            "description": t.description,
        }
        for t in THEMES.values()
    ]


# 所有主题共享的页面基础样式：Claude 写 deck 时无需再手写 .slide 容器。
# 只依赖 token，因此任何主题都成立（反色页的覆盖在 theme_css 里追加）。
_SLIDE_BASE_CSS = """
* { margin: 0; padding: 0; box-sizing: border-box; }
body { margin: 0; font-family: var(--font); }
.slide {
  width: 1920px; height: 1080px; position: relative;
  overflow: hidden; background: var(--bg); color: var(--ink);
  padding: var(--pad);
}
"""


def theme_css(name: str) -> str:
    """生成一套主题的完整 CSS 块（:root 变量 + .slide 基础样式 + 反色页覆盖）。

    Claude 在 HTML 的 <head> 写 `<style data-theme="<name>"></style>` 占位，
    deck.render 时替换成这里的输出；无占位则 append 到 </head> 前。
    """
    if name not in THEMES:
        raise KeyError(f"未知主题: {name!r}（可选: {', '.join(THEMES)}）")
    theme = THEMES[name]
    missing = [k for k in _REQUIRED_KEYS if k not in theme.base_vars]
    if missing:
        raise ValueError(f"主题 {name!r} 缺少 token: {missing}")

    # :root 必须先含公共刻度（字号/间距/行高），再被主题颜色字体覆盖。
    # 否则布局 CSS 的 var(--title) 等无定义，字号回落浏览器默认（16px）。
    lines = _fmt_vars({**_BASE_TOKENS, **theme.base_vars})
    css = ":root {\n" + lines + "}\n"
    css += _SLIDE_BASE_CSS
    if theme.variant_selector:
        vv = {**_BASE_TOKENS, **theme.variant_vars}
        css += f"\n{theme.variant_selector} {{\n" + _fmt_vars(vv) + "}\n"
    return css


def _fmt_vars(tokens: dict[str, str]) -> str:
    return "".join(f"  {k}: {v};\n" for k, v in tokens.items())


def inject_theme(html: str, name: str) -> str:
    """把主题 CSS 注入到 deck HTML 的 <head>。

    Claude 约定：在 <head> 写 `<style data-theme="<name>"></style>` 占位。
    有占位 → 填内容；无占位 → append 到 </head> 前（保底插到 <body> 前）。
    返回注入后的完整 HTML 字符串。
    """
    css = theme_css(name)
    block = f'<style data-theme="{name}">\n{css}</style>'
    pattern = re.compile(rf'(<style data-theme="{re.escape(name)}">)(.*?)(</style>)', re.S)
    if pattern.search(html):
        return pattern.sub(lambda m: f"{m.group(1)}\n{css}</style>", html, count=1)
    if "</head>" in html:
        return html.replace("</head>", block + "\n</head>", 1)
    if "<body" in html:
        return html.replace("<body", block + "\n<body", 1)
    return block + "\n" + html


# 一致性校验 / 自动选型用的 token 清单（HTML 里检测非 token 色、漂移）
TOKEN_NAMES = (
    "--bg",
    "--surface",
    "--ink",
    "--muted",
    "--accent",
    "--accent-soft",
    "--divider",
    "--font",
    "--font-serif",
    "--font-sans",
    "--kicker",
    "--title",
    "--body",
    "--caption",
    "--col-title",
    "--num",
    "--display",
    "--quote",
    "--quote-mark",
    "--tl-date",
    "--pad",
    "--gap",
    "--radius",
    "--line-height",
    "--max-line",
    "--letter-spacing",
)

# 网格基数：所有间距应对齐到 8pt（px 对应关系见 ppt_design_research.md §2.5）
GRID_UNIT = 8
