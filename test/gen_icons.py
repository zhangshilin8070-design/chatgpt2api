"""Generate Folio app launcher icons in magazine style.

Design: black square (rounded for adaptive look),
center white sans-serif "F", thin red underline at the bottom.
"""

from __future__ import annotations

import sys
from pathlib import Path

try:
    from PIL import Image, ImageDraw, ImageFont
except ImportError:
    print("PIL not installed. Run: pip install Pillow")
    sys.exit(1)


# (folder_name, size_in_px)
DENSITIES = [
    ("mipmap-mdpi", 48),
    ("mipmap-hdpi", 72),
    ("mipmap-xhdpi", 96),
    ("mipmap-xxhdpi", 144),
    ("mipmap-xxxhdpi", 192),
]

INK = (14, 14, 14, 255)
WHITE = (255, 255, 255, 255)
ACCENT = (214, 72, 59, 255)


def find_bold_font(size: int):
    candidates = [
        r"C:\Windows\Fonts\arialbd.ttf",
        r"C:\Windows\Fonts\seguibl.ttf",  # Segoe UI Black
        r"C:\Windows\Fonts\trebucbd.ttf",
        r"C:\Windows\Fonts\verdanab.ttf",
        r"C:\Windows\Fonts\msyhbd.ttc",
    ]
    for path in candidates:
        if Path(path).exists():
            try:
                return ImageFont.truetype(path, size)
            except OSError:
                continue
    return ImageFont.load_default()


def render(size: int, rounded: bool) -> Image.Image:
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)

    # Background block
    if rounded:
        # Adaptive icon round mask: keep more black area but soften corners
        radius = int(size * 0.5)
        draw.rounded_rectangle((0, 0, size - 1, size - 1), radius=radius, fill=INK)
    else:
        radius = int(size * 0.18)  # subtle 18% radius for square launcher
        draw.rounded_rectangle((0, 0, size - 1, size - 1), radius=radius, fill=INK)

    # F glyph
    font_size = int(size * 0.66)
    font = find_bold_font(font_size)
    text = "F"
    try:
        bbox = draw.textbbox((0, 0), text, font=font)
    except AttributeError:
        # Old PIL fallback
        text_w, text_h = draw.textsize(text, font=font)
        bbox = (0, 0, text_w, text_h)
    text_w = bbox[2] - bbox[0]
    text_h = bbox[3] - bbox[1]
    text_x = (size - text_w) // 2 - bbox[0]
    text_y = (size - text_h) // 2 - bbox[1] - int(size * 0.04)
    draw.text((text_x, text_y), text, fill=WHITE, font=font)

    # Accent underline at the bottom
    bar_w = int(size * 0.32)
    bar_h = max(2, int(size * 0.045))
    bar_x = (size - bar_w) // 2
    bar_y = int(size * 0.78)
    draw.rectangle((bar_x, bar_y, bar_x + bar_w - 1, bar_y + bar_h - 1), fill=ACCENT)
    return img


def main() -> int:
    project = Path(__file__).resolve().parent.parent
    res_dir = project / "android-image-app" / "app" / "src" / "main" / "res"
    if not res_dir.exists():
        print(f"res dir not found: {res_dir}")
        return 1

    written = []
    for folder, size in DENSITIES:
        target_dir = res_dir / folder
        target_dir.mkdir(parents=True, exist_ok=True)
        sq = render(size, rounded=False)
        rd = render(size, rounded=True)
        sq_path = target_dir / "ic_launcher.png"
        rd_path = target_dir / "ic_launcher_round.png"
        sq.save(sq_path, "PNG", optimize=True)
        rd.save(rd_path, "PNG", optimize=True)
        written.append((sq_path.relative_to(project), sq_path.stat().st_size))
        written.append((rd_path.relative_to(project), rd_path.stat().st_size))

    for rel, size_bytes in written:
        print(f"{size_bytes:>6} {rel}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
