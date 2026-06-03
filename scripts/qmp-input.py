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


def abs_axis(px, py):
    return int(px * 32767 / W), int(py * 32767 / H)


class QMP:
    def __init__(self, path):
        self.s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.s.connect(path)
        self.f = self.s.makefile("rw")
        self.f.readline()  # greeting
        self.cmd("qmp_capabilities")

    def cmd(self, execute, **args):
        msg = {"execute": execute}
        if args:
            msg["arguments"] = args
        self.f.write(json.dumps(msg) + "\n")
        self.f.flush()
        # drain until a response (skip async events)
        while True:
            line = self.f.readline()
            if not line:
                return None
            obj = json.loads(line)
            if "return" in obj or "error" in obj:
                return obj

    def send_events(self, events):
        self.cmd("input-send-event", events=events)

    def move(self, px, py):
        ax, ay = abs_axis(px, py)
        self.send_events([
            {"type": "abs", "data": {"axis": "x", "value": ax}},
            {"type": "abs", "data": {"axis": "y", "value": ay}},
        ])

    def click(self, px, py, n=1):
        self.move(px, py)
        time.sleep(0.3)
        for _ in range(n):
            self.send_events([{"type": "btn", "data": {"button": "left", "down": True}}])
            time.sleep(0.08)
            self.send_events([{"type": "btn", "data": {"button": "left", "down": False}}])
            time.sleep(0.12)

    def key(self, keys):
        for k in keys:
            self.cmd("send-key", keys=[{"type": "qcode", "data": k}])
            time.sleep(0.05)

    def _combo(self, *qcodes):
        self.cmd("send-key", keys=[{"type": "qcode", "data": q} for q in qcodes])
        time.sleep(0.06)

    def type(self, s):
        for ch in s:
            if ch in CHARMAP:
                self._combo(CHARMAP[ch])
            elif ch in SHIFTMAP:
                self._combo("shift", SHIFTMAP[ch])
            elif ch.isalpha() and ch.isupper():
                self._combo("shift", ch.lower())
            else:
                self._combo(ch)

    SCALE = 2

    def ocr_find(self, word, ymin=0, ymax=10 ** 9):
        from PIL import Image, ImageOps
        ppm = "/tmp/_ocr.ppm"
        self.cmd("screendump", filename=ppm)
        time.sleep(1)
        img = Image.open(ppm).convert("L")
        # upscale + autocontrast so tesseract reads small / light-background button text
        img = ImageOps.autocontrast(img.resize((img.width * self.SCALE, img.height * self.SCALE)))
        proc = "/tmp/_ocr.png"
        img.save(proc)
        hits = []
        for psm in ("11", "6"):
            out = subprocess.run(
                ["tesseract", proc, "stdout", "--psm", psm, "tsv"],
                capture_output=True, text=True,
            ).stdout
            for ln in out.splitlines()[1:]:
                f = ln.split("\t")
                if len(f) >= 12 and f[11].strip().lower() == word.lower():
                    left, top, w, h, conf = (int(f[6]), int(f[7]), int(f[8]), int(f[9]), float(f[10]))
                    cx, cy = (left + w // 2) // self.SCALE, (top + h // 2) // self.SCALE
                    if conf >= 30 and ymin <= cy <= ymax:
                        hits.append((cx, cy, conf))
        hits.sort(key=lambda t: -t[2])  # highest confidence first
        return hits

    def ocr_text(self, ymin=0, ymax=10 ** 9):
        from PIL import Image, ImageOps
        ppm = "/tmp/_ocr.ppm"
        self.cmd("screendump", filename=ppm)
        time.sleep(1)
        img = Image.open(ppm).convert("L")
        img = ImageOps.autocontrast(img.resize((img.width * self.SCALE, img.height * self.SCALE)))
        img.save("/tmp/_ocr.png")
        out = subprocess.run(
            ["tesseract", "/tmp/_ocr.png", "stdout", "--psm", "6", "tsv"],
            capture_output=True, text=True,
        ).stdout
        words = []
        for ln in out.splitlines()[1:]:
            f = ln.split("\t")
            if len(f) >= 12 and f[11].strip():
                cy = (int(f[7]) + int(f[9]) // 2) // self.SCALE
                if ymin <= cy <= ymax and float(f[10]) >= 30:
                    words.append(f[11].strip())
        return " ".join(words)

    def ocrclick(self, word, ymin=0, ymax=10 ** 9):
        hits = self.ocr_find(word, ymin, ymax)
        if not hits:
            print("NOTFOUND %s" % word)
            return False
        cx, cy, conf = hits[0]
        self.click(cx, cy)
        print("CLICK %s (%d,%d) conf=%.0f" % (word, cx, cy, conf))
        return True

    def agree_button(self):
        # The macOS SLA "Agree" button is the one immediately right of a same-row "Disagree".
        # This excludes the body-text "...read and agree to the terms...", and when a confirm sheet
        # overlays the license pane, the topmost such pair is the active modal button (the background
        # license buttons sit lower and are greyed out). Works for the license pane (one pair) too.
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
        if ag:  # fallback: no Disagree paired — take the lowest Agree (plain license button at the bottom)
            lo = max(ag, key=lambda t: t[1])
            return lo[0], lo[1]
        return None


def main():
    sock, op = sys.argv[1], sys.argv[2]
    q = QMP(sock)
    if op == "click":
        q.click(int(sys.argv[3]), int(sys.argv[4]))
    elif op == "dclick":
        q.click(int(sys.argv[3]), int(sys.argv[4]), n=2)
    elif op == "move":
        q.move(int(sys.argv[3]), int(sys.argv[4]))
    elif op == "key":
        q.key(sys.argv[3:])
    elif op == "chord":
        q._combo(*sys.argv[3:])
    elif op == "type":
        q.type(sys.argv[3])
    elif op == "ocrclick":
        word = sys.argv[3]
        ymin = int(sys.argv[4]) if len(sys.argv) > 4 else 0
        ymax = int(sys.argv[5]) if len(sys.argv) > 5 else 10 ** 9
        sys.exit(0 if q.ocrclick(word, ymin, ymax) else 3)
    elif op == "agreebtn":
        btn = q.agree_button()
        if btn:
            q.click(*btn)
            print("AGREE (%d,%d)" % btn)
        else:
            print("NO-AGREE-BUTTON")
            sys.exit(3)
    elif op == "ocrtext":
        ymin = int(sys.argv[3]) if len(sys.argv) > 3 else 0
        ymax = int(sys.argv[4]) if len(sys.argv) > 4 else 10 ** 9
        print(q.ocr_text(ymin, ymax))
    elif op == "screendump":
        q.cmd("screendump", filename=sys.argv[3])
    else:
        sys.exit("unknown op: " + op)


if __name__ == "__main__":
    main()
