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
    elif op == "type":
        q.type(sys.argv[3])
    elif op == "screendump":
        q.cmd("screendump", filename=sys.argv[3])
    else:
        sys.exit("unknown op: " + op)


if __name__ == "__main__":
    main()
