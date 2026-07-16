#!/usr/bin/env python3
"""Minimal QMP input driver for headless macOS automation.

Drives the guest via QEMU's QMP socket: absolute mouse clicks (usb-tablet maps
the 0..32767 range onto the framebuffer) plus key presses and screendumps.

Usage:
  qmp-input.py SOCK click   PX PY      # single left click at pixel (PX,PY)
  qmp-input.py SOCK dclick  PX PY      # double click
  qmp-input.py SOCK move    PX PY      # move pointer only
  qmp-input.py SOCK key     K [K...]   # send qcode key(s), e.g. ret down spc
  qmp-input.py SOCK screendump FILE    # write framebuffer ppm
Screen size defaults to 1280x800; override with QMP_W / QMP_H env vars.
"""
from __future__ import annotations

import json
import os
import socket
import subprocess
import sys
import time

W = int(os.environ.get("QMP_W", "1280"))
H = int(os.environ.get("QMP_H", "800"))

CHARMAP = {
    " ": "spc", "\n": "ret", "\t": "tab", "-": "minus", "=": "equal",
    ".": "dot", ",": "comma", "/": "slash", ";": "semicolon",
    "'": "apostrophe", "\\": "backslash", "[": "bracket_left",
    "]": "bracket_right", "`": "grave_accent",
}
SHIFTMAP = {
    "!": "1", "@": "2", "#": "3", "$": "4", "%": "5", "^": "6", "&": "7",
    "*": "8", "(": "9", ")": "0", "_": "minus", "+": "equal", ":": "semicolon",
    '"': "apostrophe", "<": "comma", ">": "dot", "?": "slash", "~": "grave_accent",
    "{": "bracket_left", "}": "bracket_right", "|": "backslash",
}

CMD_TIMEOUT = 30.0


class QMP:
    """A QMP unix-socket session for driving guest input and screendumps."""

    SCALE = 2  # OCR upscale factor so tesseract reads small / light button text

    def __init__(self, path: str) -> None:
        self.s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            self.s.settimeout(CMD_TIMEOUT)
            self.s.connect(path)
            self.f = self.s.makefile("rw")
            self.f.readline()  # greeting
            self.cmd("qmp_capabilities")
        except OSError:
            self.close()
            raise

    def __enter__(self) -> QMP:
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()

    def close(self) -> None:
        """Close the QMP connection; safe to call more than once."""
        f = getattr(self, "f", None)
        if f is not None:
            f.close()
            self.f = None
        self.s.close()

    def cmd(self, execute: str, **args: object) -> dict | None:
        """Send a QMP command and return its response, skipping async events."""
        msg: dict[str, object] = {"execute": execute}
        if args:
            msg["arguments"] = args
        self.f.write(json.dumps(msg) + "\n")
        self.f.flush()
        while True:
            line = self.f.readline()
            if not line:
                return None
            obj = json.loads(line)
            if "return" in obj or "error" in obj:
                return obj

    def send_events(self, events: list[dict]) -> None:
        self.cmd("input-send-event", events=events)

    def move(self, px: int, py: int) -> None:
        ax, ay = abs_axis(px, py)
        self.send_events([
            {"type": "abs", "data": {"axis": "x", "value": ax}},
            {"type": "abs", "data": {"axis": "y", "value": ay}},
        ])

    def click(self, px: int, py: int, n: int = 1) -> None:
        self.move(px, py)
        time.sleep(0.3)
        for _ in range(n):
            self.send_events([{"type": "btn", "data": {"button": "left", "down": True}}])
            time.sleep(0.08)
            self.send_events([{"type": "btn", "data": {"button": "left", "down": False}}])
            time.sleep(0.12)

    def key(self, keys: list[str]) -> None:
        for k in keys:
            self.cmd("send-key", keys=[{"type": "qcode", "data": k}])
            time.sleep(0.05)

    def combo(self, *qcodes: str) -> None:
        """Send qcodes as one chord (all down together)."""
        self.cmd("send-key", keys=[{"type": "qcode", "data": q} for q in qcodes])
        time.sleep(0.06)

    def type(self, s: str) -> None:
        for ch in s:
            if ch in CHARMAP:
                self.combo(CHARMAP[ch])
            elif ch in SHIFTMAP:
                self.combo("shift", SHIFTMAP[ch])
            elif ch.isalpha() and ch.isupper():
                self.combo("shift", ch.lower())
            else:
                self.combo(ch)

    def ocr_find(self, word: str, ymin: int = 0, ymax: int = 10 ** 9) -> list[tuple[int, int, float]]:
        """Return (cx, cy, confidence) for each match of word, highest confidence first."""
        hits = []
        for psm in ("11", "6"):
            for f in self._tesseract_tsv(psm):
                if f[11].strip().lower() != word.lower():
                    continue
                left, top, w, h, conf = int(f[6]), int(f[7]), int(f[8]), int(f[9]), float(f[10])
                cx, cy = (left + w // 2) // self.SCALE, (top + h // 2) // self.SCALE
                if conf >= 30 and ymin <= cy <= ymax:
                    hits.append((cx, cy, conf))
        hits.sort(key=lambda t: -t[2])
        return hits

    def ocr_text(self, ymin: int = 0, ymax: int = 10 ** 9) -> str:
        """Return the visible words in [ymin, ymax], left-to-right, space-joined."""
        words = []
        for f in self._tesseract_tsv("6"):
            if not f[11].strip():
                continue
            cy = (int(f[7]) + int(f[9]) // 2) // self.SCALE
            if ymin <= cy <= ymax and float(f[10]) >= 30:
                words.append(f[11].strip())
        return " ".join(words)

    def ocrclick(self, word: str, ymin: int = 0, ymax: int = 10 ** 9) -> bool:
        hits = self.ocr_find(word, ymin, ymax)
        if not hits:
            print("NOTFOUND %s" % word)
            return False
        cx, cy, conf = hits[0]
        self.click(cx, cy)
        print("CLICK %s (%d,%d) conf=%.0f" % (word, cx, cy, conf))
        return True

    def ocrdclick(self, word: str, yoff: int = 0) -> bool:
        """OCR-locate word and double-click at its center (+yoff to hit an icon above its label).

        Boots an OpenCore/OpenCanopy picker entry by name — its arrow keys are
        unreliable but the picker takes mouse input.
        """
        hits = self.ocr_find(word)
        if not hits:
            print("NOTFOUND %s" % word)
            return False
        cx, cy, conf = hits[0]
        self.click(cx, cy + yoff, n=2)
        print("DCLICK %s (%d,%d) conf=%.0f" % (word, cx, cy + yoff, conf))
        return True

    def agree_button(self) -> tuple[int, int] | None:
        """Locate the macOS SLA "Agree" button: the one immediately right of a same-row "Disagree".

        This excludes the body-text "...read and agree to the terms..."; when a
        confirm sheet overlays the license pane, the topmost such pair is the
        active modal button (background license buttons sit lower, greyed out).
        """
        ag = self.ocr_find("Agree")
        dis = self.ocr_find("Disagree")
        pairs = []
        for ax, ay, _ in ag:
            for dx, dy, _ in dis:
                if abs(ay - dy) <= 14 and 0 < ax - dx <= 320:
                    pairs.append((ay, ax))
                    break
        if pairs:
            pairs.sort()  # smallest y first = topmost active button row
            return pairs[0][1], pairs[0][0]
        if ag:  # no Disagree paired: take the lowest Agree (plain license button at the bottom)
            lo = max(ag, key=lambda t: t[1])
            return lo[0], lo[1]
        return None

    def _tesseract_tsv(self, psm: str) -> list[list[str]]:
        """Screendump, upscale for OCR, and return tesseract TSV rows (minus the header)."""
        # PIL is imported lazily: only the OCR ops need it, not the plain input path.
        from PIL import Image, ImageOps

        ppm = "/tmp/_ocr.ppm"
        png = "/tmp/_ocr.png"
        self.cmd("screendump", filename=ppm)
        time.sleep(1)
        img = Image.open(ppm).convert("L")
        img = ImageOps.autocontrast(img.resize((img.width * self.SCALE, img.height * self.SCALE)))
        img.save(png)
        proc = subprocess.run(
            ["tesseract", png, "stdout", "--psm", psm, "tsv"],
            capture_output=True, text=True, check=False,
        )
        if proc.returncode != 0:
            raise RuntimeError(f"tesseract failed (psm {psm}): {proc.stderr.strip()}")
        return [f for ln in proc.stdout.splitlines()[1:] if len(f := ln.split("\t")) >= 12]


def abs_axis(px: int, py: int) -> tuple[int, int]:
    return int(px * 32767 / W), int(py * 32767 / H)


def main() -> None:
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    sock, op = sys.argv[1], sys.argv[2]
    args = sys.argv[3:]
    with QMP(sock) as q:
        if op == "click":
            q.click(int(args[0]), int(args[1]))
        elif op == "dclick":
            q.click(int(args[0]), int(args[1]), n=2)
        elif op == "move":
            q.move(int(args[0]), int(args[1]))
        elif op == "key":
            q.key(args)
        elif op == "chord":
            q.combo(*args)
        elif op == "type":
            q.type(args[0])
        elif op == "ocrclick":
            ymin = int(args[1]) if len(args) > 1 else 0
            ymax = int(args[2]) if len(args) > 2 else 10 ** 9
            sys.exit(0 if q.ocrclick(args[0], ymin, ymax) else 3)
        elif op == "ocrdclick":
            yoff = int(args[1]) if len(args) > 1 else 0
            sys.exit(0 if q.ocrdclick(args[0], yoff) else 3)
        elif op == "agreebtn":
            btn = q.agree_button()
            if btn:
                q.click(*btn)
                print("AGREE (%d,%d)" % btn)
            else:
                print("NO-AGREE-BUTTON")
                sys.exit(3)
        elif op == "ocrtext":
            ymin = int(args[0]) if args else 0
            ymax = int(args[1]) if len(args) > 1 else 10 ** 9
            print(q.ocr_text(ymin, ymax))
        elif op == "screendump":
            q.cmd("screendump", filename=args[0])
        else:
            sys.exit("unknown op: " + op)


if __name__ == "__main__":
    main()
