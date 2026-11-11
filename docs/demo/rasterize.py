#!/usr/bin/env python3
"""Rasterize a Fleetty demo frame.

The frame is the ANSI string produced by `fleetty demo` (`-tags demo`), which
is the same View() the TUI draws. This script paints that string with
JetBrains Mono on the theme background from viewColors().
"""

import argparse
import sys
from pathlib import Path

try:
    from PIL import Image, ImageDraw, ImageFont
except ImportError:
    sys.stderr.write("rasterize.py: install Pillow (python3 -m pip install pillow)\n")
    sys.exit(1)

THEMES = {
    "dark": ("#0E1117", "#D7DAE0"),
    "light": ("#F4F1EA", "#263244"),
}

REGULAR = "/usr/share/fonts/truetype/jetbrains-mono/JetBrainsMono-Regular.ttf"
BOLD = "/usr/share/fonts/truetype/jetbrains-mono/JetBrainsMono-Bold.ttf"


def parse_color(value):
    value = value.lstrip("#")
    return tuple(int(value[index : index + 2], 16) for index in (0, 2, 4))


def blend(foreground, background, amount):
    return tuple(int(foreground[channel] * amount + background[channel] * (1 - amount)) for channel in range(3))


def color_256(index):
    if index < 16:
        basic = [
            (0, 0, 0),
            (205, 0, 0),
            (0, 205, 0),
            (205, 205, 0),
            (0, 0, 238),
            (205, 0, 205),
            (0, 205, 205),
            (229, 229, 229),
            (127, 127, 127),
            (255, 0, 0),
            (0, 255, 0),
            (255, 255, 0),
            (92, 92, 255),
            (255, 0, 255),
            (0, 255, 255),
            (255, 255, 255),
        ]
        return basic[index]
    if index < 232:
        index -= 16
        levels = [0, 95, 135, 175, 215, 255]
        return levels[index // 36], levels[(index // 6) % 6], levels[index % 6]
    shade = 8 + (index - 232) * 10
    return shade, shade, shade


class Cell:
    def __init__(self, char=" ", fg=None, bg=None, bold=False, dim=False):
        self.char = char
        self.fg = fg
        self.bg = bg
        self.bold = bold
        self.dim = dim


def apply_sgr(params, cell, default_fg, default_bg):
    if not params:
        params = [0]
    index = 0
    while index < len(params):
        code = params[index]
        if code == 0:
            cell.fg = None
            cell.bg = None
            cell.bold = False
            cell.dim = False
        elif code == 1:
            cell.bold = True
        elif code == 2:
            cell.dim = True
        elif code == 22:
            cell.bold = False
            cell.dim = False
        elif code == 39:
            cell.fg = None
        elif code == 49:
            cell.bg = None
        elif code in (38, 48) and index + 1 < len(params):
            kind = params[index + 1]
            target = "fg" if code == 38 else "bg"
            if kind == 2 and index + 4 < len(params):
                setattr(cell, target, (params[index + 2], params[index + 3], params[index + 4]))
                index += 4
            elif kind == 5 and index + 2 < len(params):
                setattr(cell, target, color_256(params[index + 2]))
                index += 2
        elif 30 <= code <= 37:
            cell.fg = color_256(code - 30)
        elif 40 <= code <= 47:
            cell.bg = color_256(code - 40)
        elif 90 <= code <= 97:
            cell.fg = color_256(code - 90 + 8)
        elif 100 <= code <= 107:
            cell.bg = color_256(code - 100 + 8)
        index += 1
    return cell


def parse_frame(text, default_fg, default_bg):
    rows = [[]]
    style = Cell(fg=None, bg=None)
    index = 0
    while index < len(text):
        char = text[index]
        if char == "\x1b" and index + 1 < len(text) and text[index + 1] == "[":
            end = index + 2
            while end < len(text) and not ("@" <= text[end] <= "~"):
                end += 1
            if end < len(text) and text[end] == "m":
                body = text[index + 2 : end]
                params = []
                if body:
                    for part in body.split(";"):
                        if part == "":
                            params.append(0)
                        else:
                            try:
                                params.append(int(part))
                            except ValueError:
                                params.append(0)
                apply_sgr(params, style, default_fg, default_bg)
            index = end + 1
            continue
        if char == "\x1b" and index + 1 < len(text) and text[index + 1] == "]":
            end = index + 2
            while end < len(text) and text[end] not in "\x07\x1b":
                end += 1
            if end < len(text) and text[end] == "\x1b" and end + 1 < len(text) and text[end + 1] == "\\":
                end += 1
            index = end + 1
            continue
        if char == "\n":
            rows.append([])
            index += 1
            continue
        if char == "\r":
            index += 1
            continue
        rows[-1].append(Cell(char, style.fg, style.bg, style.bold, style.dim))
        index += 1
    if rows and not rows[-1]:
        rows.pop()
    width = max((len(row) for row in rows), default=0)
    return rows, width


def paint(rows, width, background, foreground, font_size, padding):
    regular = ImageFont.truetype(REGULAR, font_size)
    bold = ImageFont.truetype(BOLD, font_size)
    cell_width = max(1, round(regular.getlength("M")))
    cell_height = font_size + 6
    image = Image.new(
        "RGB",
        (width * cell_width + padding * 2, len(rows) * cell_height + padding * 2),
        background,
    )
    draw = ImageDraw.Draw(image)
    for y, row in enumerate(rows):
        for x, cell in enumerate(row):
            origin_x = padding + x * cell_width
            origin_y = padding + y * cell_height
            fill = cell.bg if cell.bg is not None else background
            if fill != background:
                draw.rectangle(
                    (origin_x, origin_y, origin_x + cell_width, origin_y + cell_height),
                    fill=fill,
                )
            ink = cell.fg if cell.fg is not None else foreground
            if cell.dim:
                ink = blend(ink, fill, 0.72)
            if cell.char == " ":
                continue
            font = bold if cell.bold else regular
            draw.text(
                (origin_x + cell_width / 2, origin_y + cell_height / 2),
                cell.char,
                font=font,
                fill=ink,
                anchor="mm",
            )
    return image


def main():
    parser = argparse.ArgumentParser(description="Rasterize a Fleetty demo ANSI frame")
    parser.add_argument("frame")
    parser.add_argument("output")
    parser.add_argument("--theme", choices=sorted(THEMES), default="dark")
    parser.add_argument("--font-size", type=int, default=16)
    parser.add_argument("--padding", type=int, default=28)
    args = parser.parse_args()
    background, foreground = (parse_color(value) for value in THEMES[args.theme])
    text = Path(args.frame).read_text(encoding="utf-8")
    rows, width = parse_frame(text, foreground, background)
    image = paint(rows, width, background, foreground, args.font_size, args.padding)
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    image.save(output)


if __name__ == "__main__":
    main()
