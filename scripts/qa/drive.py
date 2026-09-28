#!/usr/bin/env python3
"""Drive kiln through one scenario in a real terminal window and capture it.

    scripts/qa/drive.py --terminal iterm-dark qa/scenarios/startup/welcome.steps
    scripts/qa/drive.py --terminal terminal --out qa/runs/manual qa/scenarios/.../x.steps

Input goes through Orca (`orca computer press-key|hotkey|type-text|scroll|
click`): real macOS key events, so the terminal's own key encoder runs
before kiln sees anything. AppleScript is used only for what Orca cannot do
(its capability report says windows.focus and windows.moveResize are
unsupported): creating, sizing, activating and closing windows, and reading
the terminal's own screen text. Nothing is ever written into a running
kiln's input except through Orca.

Safety: kiln runs with a scratch HOME (no real ~/.claude or ~/.harness),
HARNESS_OFFLINE=1 (faux and ollama only) and --model faux/faux-1 against a
faux server started from the scenario's script.

Scenario file (.steps): directives, then steps, one per line; `#` comments.

    @faux <script.yaml>          faux script (required)
    @fixture <dir>               project contents (default: empty project)
    @size <cols>x<rows>          window size (default 120x40; --size overrides)
    @args <kiln args...>         extra kiln arguments
    @env KEY=VAL                 extra environment (repeatable)
    @untrusted                   do not pre-trust the project
    @nogit                       do not git-init the project
    @real                        drive the real model instead of faux (no
                                 @faux); HOME is the real one unless
                                 `@home scratch` (a scratch HOME holding a
                                 copy of kiln's credentials only)
    @model <provider/model>      model for @real (default
                                 anthropic/claude-opus-4-8, effort medium)
    @setup <shell command>       run in the project before launch
                                 (repeatable; {PROJ}/{HOME}/{ROOT} expand)
    @pre <kiln args...>          run `kiln <args>` once before launch, same
                                 env (e.g. -p "task" to seed a recent session);
                                 it consumes faux steps like any other request

    {ROOT}, {PROJ} and {HOME} in @args, @env and @pre expand to the repo root,
    the scratch project and the scratch home.

    TYPE <text>                  type text (Orca type-text)
    KEY <name> [name...]         enter esc tab backspace delete up down left
                                 right home end pgup pgdown, shift+tab,
                                 shift+enter, ctrl+<x>, alt+<x>, shift+<arrow>
    SCROLL <up|down> <n>         mouse wheel over the window centre (moves
                                 the pointer there and back)
    CLICK <x> <y>                click at window-local points
    RESIZE <cols> <rows>         resize the window
    WAIT <seconds>               sleep
    WAITFOR /regex/ [seconds]    poll the screen text until it matches
    SHOT <name>                  screenshot (PNG) plus screen text (.txt)
    EXPECT /regex/               record a pass/fail against the screen text
    EXPECT_NOT /regex/           the inverse
    NOTE <text>                  write a line to the run log
    SEND <text>                  type text, then enter
    IDLE [seconds]               @real: wait until the turn is done,
                                 answering prompts like a user who trusts the
                                 plan (plan -> 1, permission -> "don't ask
                                 again" or yes, a dangerous command -> esc);
                                 progress shots every 60s (default 1200s)
    TURN <text>                  SEND then IDLE
    RUN <shell command>          run in the project (after {PROJ}/{HOME}/
                                 {ROOT} expansion) and record pass/fail on its
                                 exit status, output in the run log
    RELAUNCH [kiln args...]      exit kiln like a user (ctrl+c twice), then
                                 start it again in a new window in the same
                                 project with these extra args (e.g. -c)

Output: <out>/<terminal>/<scenario>/ holding NN-<name>.png, NN-<name>.txt,
run.log and result.json.
"""

import argparse
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
ORCA = shutil.which("orca") or "/Applications/Orca.app/Contents/Resources/bin/orca"

LIGHT_PROFILE_NAME = "kiln-qa-light"
LIGHT_PROFILE_FILE = Path.home() / "Library/Application Support/iTerm2/DynamicProfiles/kiln-qa.json"


class DriveError(Exception):
    """A driving failure that must stop the scenario rather than be worked around."""


def run(cmd, **kw):
    kw.setdefault("capture_output", True)
    kw.setdefault("text", True)
    return subprocess.run(cmd, **kw)


def osa(script, *args):
    p = run(["osascript", "-", *args], input=script)
    if p.returncode != 0:
        raise DriveError("osascript failed: " + p.stderr.strip())
    return p.stdout.strip()


# --------------------------------------------------------------------------
# Orca


def orca(*args, timeout=60):
    p = run([ORCA, "computer", *args, "--json"], timeout=timeout)
    try:
        return json.loads(p.stdout)
    except json.JSONDecodeError:
        raise DriveError("orca returned no JSON for %s: %s %s" % (args[:1], p.stdout[:200], p.stderr[:200]))


def orca_windows(bundle):
    d = orca("list-windows", "--app", bundle)
    if not d.get("ok"):
        return []
    return [w["id"] for w in d["result"]["windows"]]


def wheel_binary():
    """Compile scripts/qa/wheel.swift once (rebuilt when the source changes)."""
    src = os.path.join(os.path.dirname(os.path.abspath(__file__)), "wheel.swift")
    out = os.path.join(os.path.expanduser("~/Library/Caches/kiln-qa"), "wheel")
    if not os.path.exists(out) or os.path.getmtime(out) < os.path.getmtime(src):
        os.makedirs(os.path.dirname(out), exist_ok=True)
        p = run(["swiftc", "-O", "-o", out, src])
        if p.returncode != 0:
            raise DriveError("wheel.swift failed to compile: " + p.stderr.strip()[:300])
    return out


# Named keys -> (orca action, orca key)
PRESS = {
    "enter": "Return", "return": "Return", "esc": "Escape", "escape": "Escape",
    "tab": "Tab", "backspace": "Backspace", "delete": "ForwardDelete",
    "up": "Up", "down": "Down", "left": "Left", "right": "Right",
    "home": "Home", "end": "End", "pgup": "PageUp", "pgdown": "PageDown",
    "space": "Space",
}
MODS = {"shift": "Shift", "ctrl": "Control", "control": "Control", "alt": "Alt",
        "option": "Alt", "cmd": "Command"}


def key_action(name):
    n = name.lower()
    if n in PRESS:
        return ("press-key", PRESS[n])
    if "+" in n:
        *mods, base = n.split("+")
        base_key = PRESS.get(base, base.upper() if len(base) == 1 else base.capitalize())
        if base == "enter":
            base_key = "Return"
        return ("hotkey", "+".join(MODS[m] for m in mods) + "+" + base_key)
    if len(name) == 1:
        return ("type-text", name)
    raise DriveError("unknown key name %r" % name)


# --------------------------------------------------------------------------
# Terminal adapters


class Iterm:
    bundle = "com.googlecode.iterm2"

    def __init__(self, profile=None):
        self.profile = profile
        self.tty = None
        self.wid = None

    def launch(self, command, cols, rows):
        before = set(orca_windows(self.bundle))
        make = ('create window with profile "%s"' % self.profile) if self.profile else "create window with default profile"
        self.tty = osa(
            'on run argv\n tell application "iTerm2"\n  set w to (%s)\n'
            '  tell current session of w\n   set columns to (item 2 of argv as integer)\n'
            '   set rows to (item 3 of argv as integer)\n   write text (item 1 of argv)\n'
            '   return tty\n  end tell\n end tell\nend run' % make,
            command, str(cols), str(rows))
        self.wid = _new_window(self.bundle, before)

    def _session(self, body):
        return osa(
            'on run argv\n tell application "iTerm2"\n  repeat with w in windows\n'
            '   repeat with t in tabs of w\n    repeat with s in sessions of t\n'
            '     if tty of s is (item 1 of argv) then\n%s\n     end if\n'
            '    end repeat\n   end repeat\n  end repeat\n end tell\n return "missing"\nend run' % body,
            self.tty)

    def activate(self):
        r = self._session('      tell application "iTerm2" to activate\n      select w\n'
                          '      tell t to select\n      tell s to select\n      return "ok"')
        if r != "ok":
            raise DriveError("iTerm2 session %s not found for activation" % self.tty)

    def resize(self, cols, rows):
        osa('on run argv\n tell application "iTerm2"\n  repeat with w in windows\n'
            '   repeat with t in tabs of w\n    repeat with s in sessions of t\n'
            '     if tty of s is (item 1 of argv) then\n'
            '      set columns of s to (item 2 of argv as integer)\n'
            '      set rows of s to (item 3 of argv as integer)\n      return "ok"\n'
            '     end if\n    end repeat\n   end repeat\n  end repeat\n end tell\nend run',
            self.tty, str(cols), str(rows))

    def text(self):
        return self._session("      return contents of s")

    def centre(self):
        """The window's centre in global screen points (top-left origin)."""
        b = self._session("      return bounds of w")
        x1, y1, x2, y2 = (int(v) for v in b.split(","))
        return (x1 + x2) // 2, (y1 + y2) // 2

    def close(self):
        try:
            self._session("      tell s to close\n      return \"ok\"")
        except DriveError:
            pass


class TerminalApp:
    bundle = "com.apple.Terminal"

    def __init__(self):
        self.tty = None
        self.wid = None

    def launch(self, command, cols, rows):
        before = set(orca_windows(self.bundle))
        self.tty = osa(
            'on run argv\n tell application "Terminal"\n  activate\n'
            '  set t to do script (item 1 of argv)\n'
            '  set number of columns of t to (item 2 of argv as integer)\n'
            '  set number of rows of t to (item 3 of argv as integer)\n'
            '  return tty of t\n end tell\nend run',
            command, str(cols), str(rows))
        self.wid = _new_window(self.bundle, before)

    def _tab(self, body):
        return osa(
            'on run argv\n tell application "Terminal"\n  repeat with w in windows\n'
            '   repeat with t in tabs of w\n    if tty of t is (item 1 of argv) then\n%s\n'
            '    end if\n   end repeat\n  end repeat\n end tell\n return "missing"\nend run' % body,
            self.tty)

    def activate(self):
        r = self._tab('     tell application "Terminal" to activate\n'
                      '     set index of w to 1\n     set selected tab of w to t\n     return "ok"')
        if r != "ok":
            raise DriveError("Terminal.app tab %s not found for activation" % self.tty)

    def resize(self, cols, rows):
        osa('on run argv\n tell application "Terminal"\n  repeat with w in windows\n'
            '   repeat with t in tabs of w\n    if tty of t is (item 1 of argv) then\n'
            '     set number of columns of t to (item 2 of argv as integer)\n'
            '     set number of rows of t to (item 3 of argv as integer)\n     return "ok"\n'
            '    end if\n   end repeat\n  end repeat\n end tell\nend run',
            self.tty, str(cols), str(rows))

    def text(self):
        # Not `contents of t`: inside `repeat with t in ...` that dereferences
        # the loop variable and returns "tab 1 of window id N". `history` is
        # the tab's whole buffer (scrollback plus the visible screen).
        return self._tab("     return history of t")

    def centre(self):
        """The window's centre in global screen points (top-left origin)."""
        b = self._tab("     return bounds of w")
        x1, y1, x2, y2 = (int(v) for v in b.split(","))
        return (x1 + x2) // 2, (y1 + y2) // 2

    def close(self):
        try:
            self._tab('     close w saving no\n     return "ok"')
        except DriveError:
            pass


class Warp:
    """Warp has no AppleScript dictionary and draws with the GPU, so:
    windows open through its URL scheme; geometry goes through System
    Events; the shell commands that size-probe and start kiln are typed into
    Warp's own command input with Orca before kiln exists; and screen text
    comes from OCR of a screenshot (approximate, so checks are advisory)."""

    bundle = "dev.warp.Warp-Stable"
    text_is_rows = False

    def __init__(self, work):
        self.work = work
        self.wid = None
        self.tty = "warp"
        self.cell = None
        self.base = None

    def _type(self, text):
        # Re-activate and pause before each action: right after typing, Warp's
        # command suggestions can hold focus long enough for an immediate
        # Return to be refused as window_not_focused.
        for action, flag, arg in (("type-text", "--text", text), ("press-key", "--key", "Return")):
            self.activate()
            time.sleep(0.4)
            d = orca(action, "--app", self.bundle, "--window-id", str(self.wid), flag, arg)
            if not d.get("ok"):
                raise DriveError("warp %s failed: %s" % (action, json.dumps(d.get("error"))[:200]))

    def _set_size(self, w, h):
        osa('on run argv\n tell application "System Events" to tell process "Warp"\n'
            '  set size of front window to {(item 1 of argv as integer), (item 2 of argv as integer)}\n'
            ' end tell\nend run', str(int(w)), str(int(h)))
        time.sleep(0.6)

    def _probe(self):
        f = self.work / "warp-size.txt"
        f.unlink(missing_ok=True)
        self._type("stty size > %s" % shlex.quote(str(f)))
        for _ in range(30):
            if f.exists() and f.read_text().strip():
                rows, cols = (int(v) for v in f.read_text().split())
                return cols, rows
            time.sleep(0.2)
        raise DriveError("warp size probe produced nothing")

    def launch(self, command, cols, rows):
        before = set(orca_windows(self.bundle))
        # The bare warp://action/new_window URL opens nothing; it needs a path.
        run(["open", "warp://action/new_window?path=%s" % self.work])
        self.wid = _new_window(self.bundle, before, timeout=15)
        time.sleep(1.5)
        self._set_size(1280, 800)
        c1, r1 = self._probe()
        self._set_size(1280 + 240, 800 + 160)
        c2, r2 = self._probe()
        if c2 == c1 or r2 == r1:
            raise DriveError("warp calibration failed: %dx%d then %dx%d" % (c1, r1, c2, r2))
        self.cell = (240.0 / (c2 - c1), 160.0 / (r2 - r1))
        self.base = (1280, 800, c1, r1)
        self.resize(cols, rows)
        got = self._probe()
        self.actual = got
        self._type("clear; %s" % shlex.quote(command))

    def activate(self):
        osa('tell application "Warp" to activate\n'
            'tell application "System Events" to tell process "Warp" to perform action "AXRaise" of front window')

    def centre(self):
        """The front window's centre in global screen points, via System
        Events (Warp has no AppleScript dictionary)."""
        r = osa('tell application "System Events" to tell process "Warp"\n'
                ' set {x, y} to position of front window\n set {w, h} to size of front window\n'
                ' return (x as text) & "," & (y as text) & "," & (w as text) & "," & (h as text)\n'
                'end tell')
        x, y, w, h = (int(float(v)) for v in r.split(","))
        return x + w // 2, y + h // 2

    def resize(self, cols, rows):
        w0, h0, c0, r0 = self.base
        self._set_size(w0 + (cols - c0) * self.cell[0] + 1, h0 + (rows - r0) * self.cell[1] + 1)

    def text(self):
        d = orca("get-app-state", "--app", self.bundle, "--window-id", str(self.wid))
        src = (d.get("result") or {}).get("screenshot", {}).get("path")
        if not src:
            return ""
        p = run([str(ROOT / "bin/qa-ocr"), src])
        return p.stdout

    def close(self):
        # Orca's Command+W reports window_not_found on Warp; the window's own
        # close button through System Events is reliable.
        try:
            self.activate()
            time.sleep(0.3)
            osa('tell application "System Events" to tell process "Warp" to '
                'click (first button of front window whose subrole is "AXCloseButton")')
            time.sleep(1)
        except DriveError:
            pass


def _new_window(bundle, before, timeout=10):
    deadline = time.time() + timeout
    while time.time() < deadline:
        new = [w for w in orca_windows(bundle) if w not in before]
        if new:
            return new[0]
        time.sleep(0.3)
    raise DriveError("no new %s window appeared" % bundle)


def ensure_light_profile():
    """Install the kiln-qa-light iTerm2 dynamic profile (a separate file; the
    user's Default profile is never edited). Removed by --cleanup-profile."""
    if LIGHT_PROFILE_FILE.exists():
        return

    def c(hexv):
        r, g, b = (int(hexv[i:i + 2], 16) / 255 for i in (1, 3, 5))
        return {"Red Component": r, "Green Component": g, "Blue Component": b,
                "Alpha Component": 1, "Color Space": "sRGB"}

    ansi = ["#1f1f1f", "#b3261e", "#2e7d32", "#8a6d00", "#1e5aa8", "#7b3fa0", "#1f7a7a", "#bdbdbd",
            "#5f5f5f", "#d32f2f", "#43a047", "#a58400", "#2f74d0", "#9c52c9", "#2a9d9d", "#ffffff"]
    profile = {
        "Name": LIGHT_PROFILE_NAME, "Guid": "kiln-qa-light-0001",
        "Dynamic Profile Parent Name": "Default",
        "Background Color": c("#f7f4ee"), "Foreground Color": c("#2b2621"),
        "Bold Color": c("#2b2621"), "Cursor Color": c("#2b2621"),
        "Selection Color": c("#d9d2c5"), "Selected Text Color": c("#2b2621"),
    }
    for i, v in enumerate(ansi):
        profile["Ansi %d Color" % i] = c(v)
    LIGHT_PROFILE_FILE.parent.mkdir(parents=True, exist_ok=True)
    LIGHT_PROFILE_FILE.write_text(json.dumps({"Profiles": [profile]}, indent=1))
    time.sleep(2)  # iTerm2 picks dynamic profiles up asynchronously


def make_terminal(name, work):
    if name == "iterm-dark":
        return Iterm()
    if name == "iterm-light":
        ensure_light_profile()
        return Iterm(LIGHT_PROFILE_NAME)
    if name == "terminal":
        return TerminalApp()
    if name == "warp":
        if not (ROOT / "bin/qa-ocr").exists():
            raise DriveError("warp needs bin/qa-ocr: swiftc -O -o bin/qa-ocr scripts/qa/ocr.swift")
        return Warp(work)
    raise DriveError("unknown terminal %r" % name)


# --------------------------------------------------------------------------
# Scenario


def parse_scenario(path):
    directives = {"env": [], "pre": [], "args": [], "setup": []}
    steps = []
    for lineno, raw in enumerate(Path(path).read_text().splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("@"):
            key, _, val = line[1:].partition(" ")
            if key in ("env", "pre", "setup"):
                directives[key].append(val.strip())
            elif key == "args":
                directives["args"] += shlex.split(val)
            else:
                directives[key] = val.strip() or True
            continue
        verb, _, rest = line.partition(" ")
        steps.append((lineno, verb.upper(), rest.strip()))
    if "faux" not in directives and "real" not in directives:
        raise DriveError("%s: @faux (or @real) is required" % path)
    return directives, steps


def parse_regex(arg):
    m = re.match(r"^/(.*)/(\s+(\S+))?$", arg)
    if not m:
        raise DriveError("expected /regex/ [timeout], got %r" % arg)
    return re.compile(m.group(1), re.S), float(m.group(3)) if m.group(3) else None


class Run:
    def __init__(self, scenario, terminal_name, out_root, size_override, keep):
        self.scenario = Path(scenario)
        self.name = self.scenario.stem
        self.area = self.scenario.parent.name
        self.terminal_name = terminal_name
        self.directives, self.steps = parse_scenario(scenario)
        size = size_override or self.directives.get("size") or "120x40"
        self.cols, self.rows = (int(x) for x in size.lower().split("x"))
        self.out = Path(out_root) / terminal_name / ("%s-%s-%dx%d" % (self.area, self.name, self.cols, self.rows))
        self.out.mkdir(parents=True, exist_ok=True)
        self.keep = keep
        self.shot_n = 0
        self.expects = []
        self.shots = []
        self.log_lines = []
        self.faux = None
        self.work = None
        self.term = None

    def log(self, msg):
        line = "%s %s" % (time.strftime("%H:%M:%S"), msg)
        self.log_lines.append(line)
        print(line, flush=True)

    # ---- setup / teardown

    def setup(self):
        self.work = Path(tempfile.mkdtemp(prefix="kiln-qa-", dir="/tmp"))
        self.real = "real" in self.directives
        if self.real and self.directives.get("home") != "scratch":
            home = Path(os.environ["HOME"])
        else:
            home = self.work / "home"
            (home / ".harness").mkdir(parents=True)
            if self.real:
                creds = Path(os.environ["HOME"]) / ".harness" / "credentials.json"
                if creds.exists():
                    shutil.copy(creds, home / ".harness" / "credentials.json")
        self.home = home
        proj = self.work / "proj"
        self.proj = proj
        fixture = self.directives.get("fixture")
        if fixture and fixture is not True:
            shutil.copytree(ROOT / fixture, proj)
        else:
            proj.mkdir()
        if "untrusted" not in self.directives:
            trusted = home / ".harness" / "trusted.json"
            entries = json.loads(trusted.read_text()) if trusted.exists() else []
            for p in (str(proj), str(proj.resolve())):
                if p not in entries:
                    entries.append(p)
            trusted.write_text(json.dumps(entries, indent=2))
        if "nogit" not in self.directives:
            git = ["git", "-c", "user.name=kiln qa", "-c", "user.email=qa@example.invalid"]
            run(git + ["init", "-q", "-b", "main"], cwd=proj)
            run(git + ["add", "-A"], cwd=proj)
            run(git + ["commit", "-q", "--allow-empty", "-m", "fixture"], cwd=proj)

        def expand(s):
            return s.replace("{ROOT}", str(ROOT)).replace("{PROJ}", str(proj)).replace("{HOME}", str(home))

        for cmd in self.directives["setup"]:
            p = run(["/bin/sh", "-c", expand(cmd)], cwd=proj, env={**os.environ, "HOME": str(home)}, timeout=300)
            self.log("setup `%s` exit=%d%s" % (cmd[:80], p.returncode, (" " + (p.stderr or p.stdout).strip()[:300]) if p.returncode else ""))
            if p.returncode:
                raise DriveError("setup failed: %s" % cmd)

        if self.real:
            return self._launch_real(home, proj, expand)

        faux_log = self.work / "faux.log"
        self.faux = subprocess.Popen([str(ROOT / "bin/faux"), str(ROOT / self.directives["faux"])],
                                     stdout=open(faux_log, "w"), stderr=subprocess.STDOUT)
        addr = ""
        for _ in range(50):
            addr = faux_log.read_text().splitlines()[0].strip() if faux_log.read_text() else ""
            if addr:
                break
            time.sleep(0.1)
        if not addr:
            raise DriveError("faux did not report an address")

        def expand(s):
            return s.replace("{ROOT}", str(ROOT)).replace("{PROJ}", str(proj)).replace("{HOME}", str(home))

        env = {
            "HOME": str(home), "HARNESS_OFFLINE": "1", "HARNESS_FAUX_ADDR": addr,
            "HARNESS_FAUX_API": "anthropic-messages", "HARNESS_RETRY_JITTER": "0",
        }
        for kv in self.directives["env"]:
            k, _, v = kv.partition("=")
            env[k] = expand(v)
        kiln = [str(ROOT / "bin/kiln"), "--model", "faux/faux-1"]
        self.directives["args"] = [expand(a) for a in self.directives["args"]]

        for pre in self.directives["pre"]:
            # PWD as a shell `cd` would set it (run.sh does for the main
            # launch); a bare subprocess inherits the driver's own PWD and
            # kiln then resolves the cwd through /tmp's symlink instead.
            p = run(kiln + [expand(a) for a in shlex.split(pre)], cwd=proj,
                    env={**os.environ, **env, "PWD": str(proj)}, timeout=60)
            self.log("pre `%s` exit=%d%s" % (pre, p.returncode, (" stderr=" + p.stderr.strip()[:200]) if p.returncode else ""))

        launcher = self.work / "run.sh"
        exports = "".join("export %s=%s\n" % (k, shlex.quote(v)) for k, v in env.items())
        launcher.write_text("#!/bin/sh\ncd %s || exit 1\n%sexec %s\n" % (
            shlex.quote(str(proj)), exports, " ".join(shlex.quote(a) for a in kiln + self.directives["args"])))
        launcher.chmod(0o755)

        self.term = make_terminal(self.terminal_name, self.work)
        self.term.launch(str(launcher), self.cols, self.rows)
        actual = getattr(self.term, "actual", None)
        self.log("launched %s window=%s tty=%s size=%dx%d%s" % (
            self.terminal_name, self.term.wid, self.term.tty, self.cols, self.rows,
            (" (measured %dx%d)" % actual) if actual else ""))

    def _launch_real(self, home, proj, expand):
        env = {"HOME": str(home)} if home != Path(os.environ["HOME"]) else {}
        for kv in self.directives["env"]:
            k, _, v = kv.partition("=")
            env[k] = expand(v)
        model = self.directives.get("model") or "anthropic/claude-opus-4-8"
        kiln = [str(ROOT / "bin/kiln"), "--model", model]
        args = [expand(a) for a in self.directives["args"]]
        if "--effort" not in args:
            kiln += ["--effort", "medium"]
        for pre in self.directives["pre"]:
            p = run(kiln[:1] + [expand(a) for a in shlex.split(pre)], cwd=proj,
                    env={**os.environ, **env, "PWD": str(proj)}, timeout=120)
            self.log("pre `%s` exit=%d %s" % (pre, p.returncode, (p.stdout + p.stderr).strip()[:300]))
        self._real_launch = (env, kiln + args, expand)
        self._start_real([])
        self.log("launched real %s in %s window=%s tty=%s" % (model, proj, self.term.wid, self.term.tty))

    def _start_real(self, extra):
        env, argv, expand = self._real_launch
        launcher = self.work / "run.sh"
        exports = "".join("export %s=%s\n" % (k, shlex.quote(v)) for k, v in env.items())
        launcher.write_text("#!/bin/sh\ncd %s || exit 1\n%sexec %s\n" % (
            shlex.quote(str(self.proj)), exports, " ".join(shlex.quote(a) for a in argv + [expand(e) for e in extra])))
        launcher.chmod(0o755)
        self.term = make_terminal(self.terminal_name, self.work)
        self.term.launch(str(launcher), self.cols, self.rows)

    # ---- @real turn handling

    READY = re.compile(r"describe a task|queue a follow-up")
    BUSY = re.compile(r"esc to stop")
    # A dangerous shell command, or an MCP tool whose name says it changes
    # something (scenarios on the real HOME only ever ask read-only
    # questions of the user's own servers).
    DANGER = re.compile(r"\bsudo\b|rm -rf (/|~)|\|\s*(ba|z)?sh\b|git push|brew install|npm (i|install) -g"
                        r"|mcp__\w*(create|update|delete|remove|sync|run|install|restart|write|set|add|patch|post)", re.I)

    def decide(self, s):
        tail = "\n".join(s.split("\n")[-40:])
        if re.search(r"trust (the files in )?this folder|Do you trust", tail, re.I):
            return ("trust folder -> 2", ["2"])
        if re.search(r"Would you like to proceed\?|Ready to code\?", tail):
            return ("plan approval -> 1", ["1"])
        if re.search(r"Allow kiln to|approval needed", tail) and re.search(r"press 1, 2", tail):
            if self.DANGER.search(tail):
                return ("DANGER -> esc", ["esc"])
            if re.search(r"2\s+Yes, and don.t ask again", tail):
                return ("permission -> 2", ["2"])
            return ("permission -> 1", ["1"])
        return None

    def idle(self, timeout):
        start = time.time()
        busy_seen = False
        quiet_since = None
        last_shot = time.time()
        while True:
            if time.time() - start > timeout:
                self.shot("idle-timeout")
                raise DriveError("IDLE timed out after %ds" % timeout)
            s = self.screen()
            d = self.decide(s)
            if d:
                self.shot("prompt")
                self.log("  answer: %s" % d[0])
                for k in d[1]:
                    self.key(k)
                    time.sleep(0.3)
                time.sleep(1.5)
                quiet_since = None
                continue
            if self.BUSY.search(s):
                busy_seen = True
                quiet_since = None
                if time.time() - last_shot > 60:
                    self.shot("progress")
                    last_shot = time.time()
            else:
                quiet_since = quiet_since or time.time()
                # Done once it has been quiet long enough: 15s after real
                # work, 6s for a turn that never went busy (a slash command).
                if time.time() - quiet_since > (15 if busy_seen else 6):
                    self.log("  idle after %.0fs" % (time.time() - start))
                    return
            time.sleep(1)

    def teardown(self):
        if self.term is not None:
            if self.kiln_running():
                try:
                    self.key("ctrl+c")
                    time.sleep(0.4)
                    self.key("ctrl+c")
                except DriveError as e:
                    self.log("teardown key failed: %s" % e)
                time.sleep(1.5)
            for pid in self._kiln_pids():
                run(["kill", pid])
            time.sleep(0.3)
            self.term.close()
        if getattr(self, "real", False) and getattr(self, "home", None) == Path(os.environ["HOME"]):
            trusted = self.home / ".harness" / "trusted.json"
            try:
                entries = json.loads(trusted.read_text())
                keep = [e for e in entries if not e.startswith(str(self.work)) and not e.startswith(str(self.work.resolve()))]
                trusted.write_text(json.dumps(keep, indent=2))
            except (OSError, ValueError):
                pass
        if self.faux is not None:
            self.faux.terminate()
            try:
                self.faux.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.faux.kill()
        if self.work and not self.keep:
            # A scenario that runs go leaves a read-only module cache in its
            # scratch HOME; make entries writable and retry, and report what
            # still could not be removed instead of ignoring it.
            def writable_retry(func, path, _exc):
                os.chmod(os.path.dirname(path), 0o755)
                os.chmod(path, 0o755)
                func(path)
            try:
                shutil.rmtree(self.work, onerror=writable_retry)
            except OSError as e:
                self.log(f"cleanup: could not remove {self.work}: {e}")

    def kiln_running(self):
        return bool(self._kiln_pids())

    def _kiln_pids(self):
        """kiln processes launched by this run: run.sh exports HOME=<work>/home,
        so the scratch path appears in the process environment (`ps eww`)."""
        if self.work is None:
            return []
        p = run(["pgrep", "-f", "bin/kiln"])
        pids = []
        for pid in p.stdout.split():
            env = run(["ps", "eww", "-o", "command=", "-p", pid]).stdout
            if str(self.work) in env:
                pids.append(pid)
        return pids

    # ---- actions

    def key(self, name):
        action, arg = key_action(name)
        flag = "--text" if action == "type-text" else "--key"
        self._orca_input(action, flag, arg)

    def _orca_input(self, action, flag, arg):
        for attempt in (1, 2):
            self.term.activate()
            time.sleep(0.15)
            d = orca(action, "--app", self.term.bundle, "--window-id", str(self.term.wid), flag, arg,
                     *getattr(self.term, "input_flags", ()))
            if d.get("ok"):
                return
            code = (d.get("error") or {}).get("code")
            if code == "window_not_focused" and attempt == 1:
                self.log("orca %s %s: window_not_focused, re-activating once" % (action, arg))
                time.sleep(0.5)
                continue
            raise DriveError("orca %s %s failed: %s" % (action, arg, json.dumps(d.get("error"))[:300]))

    def buffer(self):
        """Everything the terminal holds for this session: scrollback plus the
        visible screen (iTerm2 `contents` and Terminal.app `contents` both
        include scrollback)."""
        return self.term.text()

    def screen(self):
        """Only the visible screen: the last `rows` lines of the buffer. Checks
        must never match scrollback, where stale frames can live. OCR text
        (Warp) is already just what is visible."""
        text = self.buffer()
        if not getattr(self.term, "text_is_rows", True):
            return text
        return "\n".join(text.split("\n")[-self.rows:])

    def shot(self, name):
        self.shot_n += 1
        stem = "%02d-%s" % (self.shot_n, name)
        d = orca("get-app-state", "--app", self.term.bundle, "--window-id", str(self.term.wid))
        if not d.get("ok"):
            raise DriveError("capture failed: %s" % json.dumps(d.get("error"))[:300])
        src = d["result"].get("screenshot", {}).get("path")
        if not src:
            raise DriveError("capture returned no screenshot")
        shutil.copy(src, self.out / (stem + ".png"))
        buf = self.buffer()
        visible = "\n".join(buf.split("\n")[-self.rows:])
        (self.out / (stem + ".txt")).write_text(visible)
        (self.out / (stem + ".buffer.txt")).write_text(buf)
        scrollback_rows = max(0, len(buf.split("\n")) - self.rows)
        self.shots.append({"name": name, "png": str(self.out / (stem + ".png")),
                           "txt": str(self.out / (stem + ".txt")),
                           "buffer": str(self.out / (stem + ".buffer.txt")),
                           "scrollback_rows": scrollback_rows})
        self.log("SHOT %s" % stem)

    def execute(self):
        for lineno, verb, arg in self.steps:
            self.log("%s %s" % (verb, arg))
            if verb == "TYPE":
                self._orca_input("type-text", "--text", arg)
            elif verb == "KEY":
                for name in arg.split():
                    self.key(name)
                    time.sleep(0.12)
            elif verb == "SCROLL":
                # Orca's scroll reports ok but delivers no wheel event to the
                # terminal, so SCROLL posts real wheel events itself
                # (scripts/qa/wheel.swift) over the window's centre.
                direction, n = arg.split()[:2]
                self.term.activate()
                time.sleep(0.2)
                x, y = self.term.centre()
                p = run([wheel_binary(), str(x), str(y), direction, n])
                if p.returncode != 0:
                    raise DriveError("SCROLL failed: " + (p.stderr or p.stdout).strip())
                time.sleep(0.3)
            elif verb == "CLICK":
                x, y = arg.split()[:2]
                self.term.activate()
                d = orca("click", "--app", self.term.bundle, "--window-id", str(self.term.wid), "--x", x, "--y", y)
                if not d.get("ok"):
                    raise DriveError("click failed: %s" % json.dumps(d.get("error"))[:200])
            elif verb == "RESIZE":
                c, r = (int(v) for v in arg.split()[:2])
                self.term.resize(c, r)
                self.cols, self.rows = c, r
                time.sleep(0.6)
            elif verb == "WAIT":
                time.sleep(float(arg))
            elif verb == "WAITFOR":
                rx, timeout = parse_regex(arg)
                deadline = time.time() + (timeout or 15)
                while True:
                    if rx.search(self.screen()):
                        break
                    if time.time() > deadline:
                        self.shot("waitfor-timeout-line%d" % lineno)
                        raise DriveError("line %d: WAITFOR %s timed out" % (lineno, arg))
                    time.sleep(0.4)
            elif verb == "SHOT":
                self.shot(arg or "shot")
            elif verb in ("EXPECT", "EXPECT_NOT"):
                rx, _ = parse_regex(arg)
                hit = bool(rx.search(self.screen()))
                ok = hit if verb == "EXPECT" else not hit
                advisory = not getattr(self.term, "text_is_rows", True)
                self.expects.append({"line": lineno, "verb": verb, "pattern": rx.pattern, "ok": ok,
                                     "advisory": advisory})
                self.log("  -> %s%s" % ("pass" if ok else "FAIL", " (ocr, advisory)" if advisory else ""))
            elif verb == "NOTE":
                pass
            elif verb in ("SEND", "TURN"):
                self._orca_input("type-text", "--text", arg)
                time.sleep(0.3)
                self.key("enter")
                if verb == "TURN":
                    self.idle(1200)
            elif verb == "IDLE":
                self.idle(float(arg) if arg else 1200)
            elif verb == "RUN":
                cmd = arg.replace("{ROOT}", str(ROOT)).replace("{PROJ}", str(self.proj)).replace("{HOME}", str(self.home))
                p = run(["/bin/sh", "-c", cmd], cwd=self.proj, env={**os.environ, "HOME": str(self.home)}, timeout=600)
                ok = p.returncode == 0
                self.expects.append({"line": lineno, "verb": "RUN", "pattern": arg, "ok": ok})
                self.log("  -> %s (exit %d) %s" % ("pass" if ok else "FAIL", p.returncode, (p.stdout + p.stderr).strip()[-400:]))
            elif verb == "RELAUNCH":
                if not getattr(self, "real", False):
                    raise DriveError("RELAUNCH needs @real")
                # /exit, like a user; Orca's key round trip (~0.6s each) is
                # too slow to land two ctrl+c presses inside kiln's 1s window.
                self._orca_input("type-text", "--text", "/exit")
                time.sleep(0.3)
                self.key("enter")
                deadline = time.time() + 15
                while self.kiln_running() and time.time() < deadline:
                    time.sleep(0.5)
                if self.kiln_running():
                    self.shot("exit-stuck")
                    raise DriveError("kiln did not exit on /exit within 15s")
                self.term.close()
                time.sleep(0.5)
                self._start_real(shlex.split(arg))
                self.log("  relaunched window=%s tty=%s args=%s" % (self.term.wid, self.term.tty, arg))
            else:
                raise DriveError("line %d: unknown verb %s" % (lineno, verb))

    def write_result(self, error):
        result = {
            "scenario": str(self.scenario), "terminal": self.terminal_name,
            "size": "%dx%d" % (self.cols, self.rows), "error": error,
            "expects": self.expects, "shots": self.shots,
            "text_source": "ocr" if not getattr(self.term, "text_is_rows", True) else "terminal",
            "passed": error is None and all(e["ok"] for e in self.expects if not e.get("advisory")),
        }
        (self.out / "result.json").write_text(json.dumps(result, indent=1))
        (self.out / "run.log").write_text("\n".join(self.log_lines) + "\n")
        return result


VERBS = {"TYPE", "KEY", "SCROLL", "CLICK", "RESIZE", "WAIT", "WAITFOR", "SHOT", "EXPECT", "EXPECT_NOT", "NOTE",
         "SEND", "IDLE", "TURN", "RUN", "RELAUNCH"}
FAUX_TOP = {"model", "steps", "models"}
FAUX_STEP = {"text", "thinking", "tool_call", "tool_calls", "on_tool_result", "on_tool_results",
             "then", "usage", "error", "delay", "disconnect_after", "end_turn", "chunk_delay"}
FAUX_CALL = {"name", "args", "raw_args", "id"}


def lint_faux(path):
    """Check a faux script against the real step schema
    (internal/testkit/faux/script.go): faux itself silently ignores unknown
    keys, so a typo would otherwise just produce an empty turn."""
    p = run(["yq", "-o=json", str(path)])
    if p.returncode != 0:
        return ["%s: not valid YAML: %s" % (path, p.stderr.strip()[:200])]
    doc = json.loads(p.stdout or "null")
    problems = []
    if not isinstance(doc, dict):
        return ["%s: top level must be a mapping" % path]
    for k in doc:
        if k not in FAUX_TOP:
            problems.append("%s: unknown top-level key %r" % (path, k))
    if "steps" not in doc and "models" not in doc:
        problems.append("%s: needs steps: or models:" % path)

    def steps(lst, where):
        if not isinstance(lst, list):
            problems.append("%s: %s must be a list" % (path, where))
            return
        for i, st in enumerate(lst):
            loc = "%s[%d]" % (where, i)
            if not isinstance(st, dict):
                problems.append("%s: %s must be a mapping" % (path, loc))
                continue
            for k in st:
                if k not in FAUX_STEP:
                    problems.append("%s: %s unknown step key %r" % (path, loc, k))
            calls = ([st["tool_call"]] if "tool_call" in st else []) + list(st.get("tool_calls") or [])
            for c in calls:
                for k in (c or {}):
                    if k not in FAUX_CALL:
                        problems.append("%s: %s unknown tool_call key %r" % (path, loc, k))
                if not (c or {}).get("name"):
                    problems.append("%s: %s tool_call without name" % (path, loc))
            if "then" in st:
                steps(st["then"], loc + ".then")

    if "steps" in doc:
        steps(doc["steps"], "steps")
    for name, lst in (doc.get("models") or {}).items():
        steps(lst, "models.%s" % name)
    return problems


def lint_scenario(path):
    problems = []
    try:
        directives, steps = parse_scenario(path)
    except DriveError as e:
        return [str(e)]
    if "faux" in directives:
        faux = ROOT / directives["faux"]
        if not faux.exists():
            problems.append("%s: @faux %s does not exist" % (path, directives["faux"]))
        else:
            problems += lint_faux(faux)
    fixture = directives.get("fixture")
    if fixture and fixture is not True and not (ROOT / fixture).is_dir():
        problems.append("%s: @fixture %s is not a directory" % (path, fixture))
    size = directives.get("size")
    if size and not re.match(r"^\d+x\d+$", str(size)):
        problems.append("%s: bad @size %r" % (path, size))
    shots = 0
    for lineno, verb, arg in steps:
        loc = "%s:%d" % (path, lineno)
        if verb not in VERBS:
            problems.append("%s: unknown verb %s" % (loc, verb))
            continue
        try:
            if verb == "KEY":
                for name in arg.split():
                    key_action(name)
            elif verb in ("WAITFOR", "EXPECT", "EXPECT_NOT"):
                parse_regex(arg)
            elif verb == "RESIZE":
                c, r = (int(v) for v in arg.split()[:2])
            elif verb == "SCROLL":
                d, n = arg.split()[:2]
                if d not in ("up", "down"):
                    raise DriveError("direction must be up or down")
                int(n)
            elif verb == "WAIT":
                float(arg)
            elif verb == "SHOT":
                shots += 1
                if not re.match(r"^[a-z0-9][a-z0-9-]*$", arg):
                    raise DriveError("SHOT name must be lowercase-kebab")
        except (DriveError, ValueError) as e:
            problems.append("%s: %s %s: %s" % (loc, verb, arg, e))
    if shots == 0:
        problems.append("%s: no SHOT steps; every scenario must capture evidence" % path)
    if not steps or steps[0][1] != "WAITFOR":
        problems.append("%s: first step should WAITFOR the ready screen" % path)
    return problems


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("scenarios", nargs="*")
    ap.add_argument("--terminal", default="iterm-dark", choices=["iterm-dark", "iterm-light", "terminal", "warp"])
    ap.add_argument("--out", default=str(ROOT / "qa/runs" / time.strftime("%Y%m%d-%H%M%S")))
    ap.add_argument("--size", help="override the scenario's @size, e.g. 80x24")
    ap.add_argument("--keep", action="store_true", help="keep the scratch project and home")
    ap.add_argument("--cleanup-profile", action="store_true", help="remove the kiln-qa-light iTerm2 profile and exit")
    ap.add_argument("--lint", action="store_true", help="check scenarios and their faux scripts; drive nothing")
    a = ap.parse_args()

    if a.lint:
        problems = []
        for sc in a.scenarios:
            problems += lint_scenario(sc)
        for pr in problems:
            print(pr)
        print("%d scenario(s), %d problem(s)" % (len(a.scenarios), len(problems)), file=sys.stderr)
        return 1 if problems else 0
    if a.cleanup_profile:
        LIGHT_PROFILE_FILE.unlink(missing_ok=True)
        print("removed" if not LIGHT_PROFILE_FILE.exists() else "still present")
        return 0
    if not a.scenarios:
        ap.error("no scenario given")
    for binary in ("bin/kiln", "bin/faux"):
        if not (ROOT / binary).exists():
            print("missing %s: run `make build && go build -o bin/faux ./cmd/faux`" % binary, file=sys.stderr)
            return 2

    failed = 0
    for sc in a.scenarios:
        r = Run(sc, a.terminal, a.out, a.size, a.keep)
        error = None
        try:
            r.setup()
            r.execute()
        except DriveError as e:
            error = str(e)
            r.log("ERROR " + error)
        except Exception as e:  # keep teardown running on bugs in this script
            error = "driver bug: %r" % e
            r.log("ERROR " + error)
        finally:
            r.teardown()
        res = r.write_result(error)
        failed += 0 if res["passed"] else 1
        print("%s %s -> %s" % ("PASS" if res["passed"] else "FAIL", sc, r.out))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
