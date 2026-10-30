"""Render the scenes capture.py saved as an animated GIF of a terminal.

Each scene clears the screen, types its command, and shows what the
command wrote, step by step for the keys typed in triage.
"""

import json
import sys

from PIL import Image, ImageDraw, ImageFont

FONT = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"
BOLD = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf"
SIZE = 15
LINE = 21
COLS = 112
ROWS = 22
PAD = 22
BAR = 34

BG = (30, 30, 46)
WINDOW = (24, 24, 37)
FG = (205, 214, 244)
DIM = (127, 132, 156)
GREEN = (166, 227, 161)
BLUE = (137, 180, 250)
YELLOW = (249, 226, 175)
DOTS = [(243, 139, 168), (249, 226, 175), (166, 227, 161)]

# Frame durations, in milliseconds.
TYPE = 45
AFTER_TYPING = 500
STEP = 1100
END = {0: 2600, 1: 4200, 2: 1800, 3: 2600, 4: 3600}

font = ImageFont.truetype(FONT, SIZE)
bold = ImageFont.truetype(BOLD, SIZE)
CHAR = font.getlength("M")
WIDTH = int(2 * PAD + COLS * CHAR)
HEIGHT = BAR + 2 * PAD + ROWS * LINE


def color(line):
    """Returns the color and font of an output line."""
    if line.startswith("REPO "):
        return BLUE, bold
    if line.startswith(("Would ", "Marked ", "Left ", "Run again")):
        return YELLOW, font
    if line.startswith("[o]pen"):
        return DIM, font
    if line.startswith("("):
        return BLUE, bold
    return FG, font


def frame(command, typed, output, cursor):
    img = Image.new("RGB", (WIDTH, HEIGHT), BG)
    d = ImageDraw.Draw(img)
    d.rounded_rectangle([0, 0, WIDTH - 1, HEIGHT - 1], radius=10, fill=WINDOW)
    for i, c in enumerate(DOTS):
        x = PAD + i * 20
        d.ellipse([x, BAR / 2 - 6, x + 12, BAR / 2 + 6], fill=c)
    title = "gh inbox"
    d.text(((WIDTH - font.getlength(title)) / 2, BAR / 2 - SIZE / 2 - 1), title, font=font, fill=DIM)

    lines = [None] + output  # None is the command line.
    lines = lines[-ROWS:]
    y = BAR + PAD
    for line in lines:
        if line is None:
            d.text((PAD, y), "$ ", font=bold, fill=GREEN)
            text = command[:typed]
            d.text((PAD + 2 * CHAR, y), text, font=font, fill=FG)
            if cursor:
                x = PAD + (2 + len(text)) * CHAR
                d.rectangle([x, y + 1, x + CHAR - 1, y + LINE - 3], fill=FG)
        else:
            fill, f = color(line)
            if line.startswith("[o]pen") and ": " in line:
                prompt, _, answer = line.rpartition(": ")
                d.text((PAD, y), prompt + ": ", font=font, fill=DIM)
                d.text((PAD + (len(prompt) + 2) * CHAR, y), answer, font=bold, fill=GREEN)
            else:
                d.text((PAD, y), line, font=f, fill=fill)
        y += LINE
    return img


def main():
    scenes = json.load(open(sys.argv[1]))
    frames, durations = [], []

    def add(img, ms):
        frames.append(img)
        durations.append(ms)

    for n, scene in enumerate(scenes):
        command = scene["command"]
        add(frame(command, 0, [], True), 700)
        for typed in range(1, len(command) + 1, 2):
            add(frame(command, typed, [], True), TYPE)
        add(frame(command, len(command), [], True), AFTER_TYPING)
        steps = scene["steps"]
        for i, step in enumerate(steps):
            last = i == len(steps) - 1
            add(frame(command, len(command), step, False), END.get(n, 2500) if last else STEP)

    # One palette for every frame keeps the colors steady.
    finals = [f for f, ms in zip(frames, durations) if ms >= STEP]
    sample = Image.new("RGB", (WIDTH, HEIGHT * len(finals)))
    for i, f in enumerate(finals):
        sample.paste(f, (0, i * HEIGHT))
    palette = sample.quantize(colors=64, method=Image.Quantize.MEDIANCUT)
    gif = [f.quantize(palette=palette, dither=Image.Dither.NONE) for f in frames]
    gif[0].save(sys.argv[2], save_all=True, append_images=gif[1:], duration=durations, loop=0, optimize=True, disposal=1)


if __name__ == "__main__":
    main()
