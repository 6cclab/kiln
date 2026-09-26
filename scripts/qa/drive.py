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
    @pre <kiln args...>          run `kiln <args>` once before launch, same
                                 env (e.g. -p "task" to seed a recent session);
                                 it consumes faux steps like any other request

    {ROOT}, {PROJ} and {HOME} in @args, @env and @pre expand to the repo root,
    the scratch project and the scratch home.

    TYPE <text>                  type text (Orca type-text)
    KEY <name> [name...]         enter esc tab backspace delete up down left
                                 right home end pgup pgdown, shift+tab,
                                 shift+enter, ctrl+<x>, alt+<x>, shift+<arrow>
    SCROLL <up|down> <n>         mouse wheel over the window centre
    CLICK <x> <y>                click at window-local points
    RESIZE <cols> <rows>         resize the window
    WAIT <seconds>               sleep
    WAITFOR /regex/ [seconds]    poll the screen text until it matches
    SHOT <name>                  screenshot (PNG) plus screen text (.txt)
    EXPECT /regex/               record a pass/fail against the screen text
    EXPECT_NOT /regex/           the inverse
    NOTE <text>                  write a line to the run log

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
        return self._tab("     return contents of t")

    def close(self):
        try:
            self._tab('     close w saving no\n     return "ok"')
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


def make_terminal(name):
    if name == "iterm-dark":
        return Iterm()
    if name == "iterm-light":
        ensure_light_profile()
        return Iterm(LIGHT_PROFILE_NAME)
    if name == "terminal":
        return TerminalApp()
    if name == "warp":
        raise DriveError("warp adapter not implemented yet")
    raise DriveError("unknown terminal %r" % name)


# --------------------------------------------------------------------------
# Scenario


def parse_scenario(path):
    directives = {"env": [], "pre": [], "args": []}
    steps = []
    for lineno, raw in enumerate(Path(path).read_text().splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("@"):
            key, _, val = line[1:].partition(" ")
            if key in ("env", "pre"):
                directives[key].append(val.strip())
            elif key == "args":
                directives["args"] += shlex.split(val)
            else:
                directives[key] = val.strip() or True
            continue
        verb, _, rest = line.partition(" ")
        steps.append((lineno, verb.upper(), rest.strip()))
    if "faux" not in directives:
        raise DriveError("%s: @faux is required" % path)
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
        home = self.work / "home"
        (home / ".harness").mkdir(parents=True)
        proj = self.work / "proj"
        fixture = self.directives.get("fixture")
        if fixture and fixture is not True:
            shutil.copytree(ROOT / fixture, proj)
        else:
            proj.mkdir()
        if "untrusted" not in self.directives:
            (home / ".harness" / "trusted.json").write_text(json.dumps([str(proj)]))
        if "nogit" not in self.directives:
            git = ["git", "-c", "user.name=kiln qa", "-c", "user.email=qa@example.invalid"]
            run(git + ["init", "-q", "-b", "main"], cwd=proj)
            run(git + ["add", "-A"], cwd=proj)
            run(git + ["commit", "-q", "--allow-empty", "-m", "fixture"], cwd=proj)

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
            p = run(kiln + [expand(a) for a in shlex.split(pre)], cwd=proj,
                    env={**os.environ, **env}, timeout=60)
            self.log("pre `%s` exit=%d%s" % (pre, p.returncode, (" stderr=" + p.stderr.strip()[:200]) if p.returncode else ""))

        launcher = self.work / "run.sh"
        exports = "".join("export %s=%s\n" % (k, shlex.quote(v)) for k, v in env.items())
        launcher.write_text("#!/bin/sh\ncd %s || exit 1\n%sexec %s\n" % (
            shlex.quote(str(proj)), exports, " ".join(shlex.quote(a) for a in kiln + self.directives["args"])))
        launcher.chmod(0o755)

        self.term = make_terminal(self.terminal_name)
        self.term.launch(str(launcher), self.cols, self.rows)
        self.log("launched %s window=%s tty=%s size=%dx%d" % (
            self.terminal_name, self.term.wid, self.term.tty, self.cols, self.rows))

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
        if self.faux is not None:
            self.faux.terminate()
            try:
                self.faux.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.faux.kill()
        if self.work and not self.keep:
            shutil.rmtree(self.work, ignore_errors=True)

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
            d = orca(action, "--app", self.term.bundle, "--window-id", str(self.term.wid), flag, arg)
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
        must never match scrollback, where stale frames can live."""
        lines = self.buffer().split("\n")
        return "\n".join(lines[-self.rows:])

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
                direction, n = arg.split()[:2]
                for _ in range(int(n)):
                    self.term.activate()
                    d = orca("scroll", "--app", self.term.bundle, "--window-id", str(self.term.wid),
                             "--x", "400", "--y", "300", "--direction", direction)
                    if not d.get("ok"):
                        raise DriveError("scroll failed: %s" % json.dumps(d.get("error"))[:200])
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
                self.expects.append({"line": lineno, "verb": verb, "pattern": rx.pattern, "ok": ok})
                self.log("  -> %s" % ("pass" if ok else "FAIL"))
            elif verb == "NOTE":
                pass
            else:
                raise DriveError("line %d: unknown verb %s" % (lineno, verb))

    def write_result(self, error):
        result = {
            "scenario": str(self.scenario), "terminal": self.terminal_name,
            "size": "%dx%d" % (self.cols, self.rows), "error": error,
            "expects": self.expects, "shots": self.shots,
            "passed": error is None and all(e["ok"] for e in self.expects),
        }
        (self.out / "result.json").write_text(json.dumps(result, indent=1))
        (self.out / "run.log").write_text("\n".join(self.log_lines) + "\n")
        return result


VERBS = {"TYPE", "KEY", "SCROLL", "CLICK", "RESIZE", "WAIT", "WAITFOR", "SHOT", "EXPECT", "EXPECT_NOT", "NOTE"}
FAUX_TOP = {"model", "steps", "models"}
FAUX_STEP = {"text", "thinking", "tool_call", "tool_calls", "on_tool_result", "on_tool_results",
             "then", "usage", "error", "delay", "disconnect_after"}
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
