"""Run the demo commands against the fake API and save what a terminal shows.

Each command runs in a pseudo-terminal, so gh inbox writes what it writes to
a person: aligned tables and relative times. Keys are typed when the output
asks for them. The result is a JSON list of {"command", "steps"}, where each
step is the screen text after it, for render.py to animate.
"""

import json
import os
import pty
import select
import shlex
import struct
import sys
import termios
import fcntl
import time

COLS = 120

# Each scene is a command, and the keys to type after each triage prompt.
SCENES = [
    ("gh inbox list --review-requested @me --sort repo", []),
    ("gh inbox clean --state merged,closed --list", []),
    ("gh inbox clean --state merged,closed --apply", []),
    ("gh inbox triage --type issue --state open", ["o", "d", "s", "q"]),
    ("gh inbox list", []),
]

PROMPT = b"[q]uit: "


def screen(raw):
    """Applies the carriage returns and line clears in raw to plain lines."""
    lines, line, col = [], [], 0
    i = 0
    text = raw.decode("utf-8", "replace")
    while i < len(text):
        c = text[i]
        if c == "\x1b":
            end = i + 1
            if end < len(text) and text[end] == "[":
                end += 1
                while end < len(text) and not text[end].isalpha():
                    end += 1
                if text[end] == "K":
                    del line[col:]
            i = end + 1
            continue
        if c == "\r":
            col = 0
        elif c == "\n":
            lines.append("".join(line).rstrip())
            line, col = [], 0
        else:
            if col < len(line):
                line[col] = c
            else:
                line.append(c)
            col += 1
        i += 1
    if line:
        lines.append("".join(line).rstrip())
    return lines


def run(binary, command, keys):
    args = [binary] + shlex.split(command)[2:]
    pid, fd = pty.fork()
    if pid == 0:
        os.execv(binary, args)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 50, COLS, 0, 0))
    out, steps, keys = b"", [], list(keys)
    while True:
        r, _, _ = select.select([fd], [], [], 0.5)
        if r:
            try:
                data = os.read(fd, 4096)
            except OSError:
                break
            if not data:
                break
            out += data
            continue
        # Quiet: if a triage prompt waits for a key, type the next one.
        if keys and out.rstrip(b" ").endswith(PROMPT.rstrip(b" ")):
            steps.append(screen(out))
            os.write(fd, keys.pop(0).encode())
            time.sleep(0.3)
    os.waitpid(pid, 0)
    steps.append(screen(out))
    return steps


def main():
    binary, dest = sys.argv[1], sys.argv[2]
    scenes = []
    for command, keys in SCENES:
        scenes.append({"command": command, "steps": run(binary, command, keys)})
    with open(dest, "w") as f:
        json.dump(scenes, f, indent=1)


if __name__ == "__main__":
    main()
