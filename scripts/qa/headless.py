"""Headless terminal backend for scripts/qa/drive.py (`--terminal xterm-dark`
or `xterm-light`), so most QA scenarios can run in CI (a Linux runner has no
iTerm2/Terminal.app/Warp) instead of only on a Mac.

kiln runs in a real PTY (Python's `pty.fork`, which gives the child its own
session and controlling tty; TERM=xterm-256color, COLORTERM=truecolor). The
PTY is rendered by xterm.js running in headless Chromium via Playwright
(`scripts/qa/xterm/index.html`, a static page — no bundler; its node_modules
pin the xterm.js and font packages so glyph rendering is the same on macOS
and Linux). A local WebSocket bridges the two: a background thread reads the
PTY's master fd and pushes bytes to the page as binary frames (so kiln's
multi-byte UTF-8 output never crosses a text-frame re-encode); the page's
own xterm.js `onData` callback pushes back whatever it generates from real
keyboard/mouse events as text frames, UTF-8-encoded by the browser same as
the long-standing xterm.js/node-pty `xterm-addon-attach` convention.

Playwright's sync API is only used on the thread that owns it (input,
resize, screenshots, buffer reads): scripts/qa/drive.py's `-j` parallelism
for headless terminals spawns separate `drive.py` subprocesses instead of
threads, because Playwright's sync API is not thread-safe.

Input goes through xterm.js's own key encoder, never by writing bytes to
the PTY directly: `type_text` uses `page.keyboard.type`, `press_key` uses
`page.keyboard.press` (or a Shift/Ctrl/Alt-chord version of it) with a
mapping from drive.py's key names, `scroll` uses `page.mouse.wheel` over
the terminal's own centre (xterm.js has its own mouse-tracking support, so
with kiln's SGR mouse mode enabled it emits the same wheel reports a real
terminal would), and `click` uses `page.mouse.click`.

OSC 11 (the background-colour query `internal/tui/app.go`'s
`tea.RequestBackgroundColor` sends, so `SetTerminalBackground` can adapt
kiln's tokens to whatever background the terminal reports) is answered
directly against the raw PTY byte stream by this module's bridge thread,
*not* by xterm.js and not from the page. Two things ruled that out:
xterm.js's core does not answer an OSC 11 query on its own (checked against
`node_modules/@xterm/xterm/lib/xterm.js` in scripts/qa/xterm: it emits no
"11;rgb:" response internally), and even if a page-side responder did
answer, kiln's own reply window (`bannerBackgroundGrace` in
`internal/tui/app.go`, 30ms) is short enough that an extra WebSocket +
Chromium round trip risked missing it. Replying from the bridge thread,
right where the query is read off the PTY, is the fastest and most
deterministic path — see the "Verify" section of this feature's own report
for the evidence kiln actually picked light tokens under xterm-light.

Requires (kept optional so macOS terminals still work with nothing
installed): `pip install -r scripts/qa/requirements.txt` (playwright,
websockets) and `python -m playwright install chromium`, plus
`cd scripts/qa/xterm && npm ci`.
"""

import fcntl
import os
import pty
import signal
import socket
import struct
import termios
import threading
import time
from pathlib import Path

try:
    from playwright.sync_api import sync_playwright
except ImportError:  # pragma: no cover - exercised only when the dep is missing
    sync_playwright = None

try:
    import websockets.sync.server as ws_server
except ImportError:  # pragma: no cover
    ws_server = None

HERE = Path(__file__).resolve().parent
PAGE_DIR = HERE / "xterm"

# Kept in sync with docs/kiln-design-handoff/Terminal.dc.html and
# internal/tui/theme.go's hexDesignBg (#14110d, the design's own dark
# background); the light values match drive.py's iTerm2 kiln-qa-light
# dynamic profile (ensure_light_profile) so the two backends test the same
# two themes.
THEMES = {
    "xterm-dark": {"background": "#14110d", "foreground": "#e8e3d8"},
    "xterm-light": {"background": "#f7f4ee", "foreground": "#2b2621"},
}


class HeadlessError(Exception):
    """A headless-backend failure; scripts/qa/drive.py wraps it as a DriveError."""


def _require_deps():
    missing = []
    if sync_playwright is None:
        missing.append("playwright")
    if ws_server is None:
        missing.append("websockets")
    if missing:
        raise HeadlessError(
            "missing python package(s) %s: pip install -r scripts/qa/requirements.txt "
            "&& python -m playwright install chromium (see docs/testing.md 'Headless QA')"
            % ", ".join(missing))
    if not (PAGE_DIR / "node_modules" / "@xterm" / "xterm").exists():
        raise HeadlessError("scripts/qa/xterm/node_modules is missing: cd scripts/qa/xterm && npm ci")


def _rgb_osc_reply(hexcolor):
    """OSC 11 response body for hexcolor, e.g. '14110d' -> 'rgb:1414/1111/0d0d'
    (each 8-bit channel duplicated to 16 bits, the conventional xterm reply
    precision; internal/tui/theme.go's own comment on hexDesignBg notes
    terminal background reports are effectively 8-bit anyway)."""
    h = hexcolor.lstrip("#")
    r, g, b = h[0:2], h[2:4], h[4:6]
    return ("\x1b]11;rgb:%s%s/%s%s/%s%s\x1b\\" % (r, r, g, g, b, b)).encode("ascii")


KEY_MAP = {
    "enter": "Enter", "return": "Enter", "esc": "Escape", "escape": "Escape",
    "tab": "Tab", "backspace": "Backspace", "delete": "Delete",
    "up": "ArrowUp", "down": "ArrowDown", "left": "ArrowLeft", "right": "ArrowRight",
    "home": "Home", "end": "End", "pgup": "PageUp", "pgdown": "PageDown",
    "space": "Space",
}
MOD_MAP = {"shift": "Shift", "ctrl": "Control", "control": "Control", "alt": "Alt",
           "option": "Alt", "cmd": "Meta"}


class Headless:
    """Drives kiln inside headless Chromium + xterm.js. Duck-types the same
    launch/activate/resize/text/centre/close surface scripts/qa/drive.py's
    macOS adapters (Iterm/TerminalApp/Warp) provide, plus a native input/
    capture surface (type_text/press_key/scroll/click/screenshot/child_pid/
    alive/kill) that Run prefers over the Orca path whenever it is present."""

    text_is_rows = True
    bundle = None  # no macOS app; some drive.py branches check this attribute

    def __init__(self, theme_name, work):
        _require_deps()
        if theme_name not in THEMES:
            raise HeadlessError("unknown headless theme %r" % theme_name)
        self.theme_name = theme_name
        self.theme = THEMES[theme_name]
        self.work = work
        self.wid = theme_name
        self.tty = None
        self.cols = None
        self.rows = None
        self.master_fd = None
        self.pid = None
        self._port = None
        self._ws_server = None
        self._ws_thread = None
        self._playwright = None
        self._browser = None
        self._page = None
        self._closed = False

    # ---- PTY -------------------------------------------------------------

    def _spawn_pty(self, command, cols, rows):
        pid, fd = pty.fork()
        if pid == 0:
            os.environ["TERM"] = "xterm-256color"
            os.environ["COLORTERM"] = "truecolor"
            try:
                os.execv(command, [command])
            except OSError:
                os._exit(127)
        self.pid = pid
        self.master_fd = fd
        self.tty = "headless-pty:%d" % pid
        self._set_winsize(cols, rows)

    def _set_winsize(self, cols, rows):
        packed = struct.pack("HHHH", rows, cols, 0, 0)
        fcntl.ioctl(self.master_fd, termios.TIOCSWINSZ, packed)
        if self.pid:
            try:
                os.kill(self.pid, signal.SIGWINCH)
            except ProcessLookupError:
                pass

    # ---- WebSocket bridge --------------------------------------------------

    def _start_pty_reader(self):
        """Starts reading the PTY the instant it exists, independent of
        Chromium/the WebSocket page connecting. This matters: kiln's OSC 11
        reply window (internal/tui/app.go's bannerBackgroundGrace) is 30ms
        from Init(), and launching Chromium plus navigating the page easily
        takes hundreds of milliseconds — answering the query only once a
        browser page had connected (an earlier version of this bridge did
        exactly that) reliably missed kiln's window, so every theme silently
        fell back to its unadjusted dark-design tokens. Answering here,
        right where the query is read off the PTY, lands in well under 1ms.
        Output read before any page connects is kept in self._backlog and
        replayed to the page once it does, so nothing is lost."""
        osc_query = b"\x1b]11;?"
        osc_reply = _rgb_osc_reply(self.theme["background"])
        master_fd = self.master_fd
        self._backlog = bytearray()
        self._backlog_lock = threading.Lock()
        self._live_conns = set()
        self._live_lock = threading.Lock()
        self._reader_stop = threading.Event()

        def pump():
            tail = b""
            while not self._reader_stop.is_set():
                try:
                    chunk = os.read(master_fd, 65536)
                except OSError:
                    break
                if not chunk:
                    break
                window = tail + chunk
                if osc_query in window:
                    try:
                        os.write(master_fd, osc_reply)
                    except OSError:
                        pass
                tail = window[-16:]
                with self._backlog_lock:
                    self._backlog += chunk
                with self._live_lock:
                    conns = list(self._live_conns)
                for c in conns:
                    try:
                        c.send(chunk)
                    except Exception:
                        with self._live_lock:
                            self._live_conns.discard(c)
            self._reader_stop.set()

        self._reader_thread = threading.Thread(target=pump, daemon=True)
        self._reader_thread.start()

    def _start_bridge(self):
        s = socket.socket()
        s.bind(("127.0.0.1", 0))
        self._port = s.getsockname()[1]
        s.close()
        master_fd = self.master_fd

        def handler(connection):
            with self._backlog_lock:
                backlog = bytes(self._backlog)
            if backlog:
                try:
                    connection.send(backlog)
                except Exception:
                    return
            with self._live_lock:
                self._live_conns.add(connection)
            try:
                for message in connection:
                    data = message.encode("utf-8") if isinstance(message, str) else message
                    try:
                        os.write(master_fd, data)
                    except OSError:
                        break
            except Exception:
                pass
            finally:
                with self._live_lock:
                    self._live_conns.discard(connection)

        self._ws_server = ws_server.serve(handler, "127.0.0.1", self._port)
        self._ws_thread = threading.Thread(target=self._ws_server.serve_forever, daemon=True)
        self._ws_thread.start()

    # ---- adapter surface (used by drive.py generically) --------------------

    def launch(self, command, cols, rows):
        self.cols, self.rows = cols, rows
        self._spawn_pty(command, cols, rows)
        self._start_pty_reader()
        self._start_bridge()
        self._playwright = sync_playwright().start()
        self._browser = self._playwright.chromium.launch()
        self._page = self._browser.new_page(
            viewport={"width": cols * 11 + 80, "height": rows * 24 + 80})
        bg = self.theme["background"].lstrip("#")
        fg = self.theme["foreground"].lstrip("#")
        url = "file://%s/index.html?cols=%d&rows=%d&port=%d&bg=%s&fg=%s" % (
            PAGE_DIR, cols, rows, self._port, bg, fg)
        self._page.goto(url)
        self._page.wait_for_function("window.__wsReady === true", timeout=15000)
        self._page.locator("#terminal").wait_for(state="visible", timeout=15000)
        self._page.wait_for_timeout(200)  # first paint + font load

    def activate(self):
        pass  # no window manager to raise; the page is always "active"

    def resize(self, cols, rows):
        self.cols, self.rows = cols, rows
        self._set_winsize(cols, rows)
        self._page.evaluate("([c, r]) => window.__resize(c, r)", [cols, rows])

    def text(self):
        return self._page.evaluate("() => window.__bufferText()")

    def centre(self):
        box = self._page.locator("#terminal").bounding_box()
        return (int(box["x"] + box["width"] / 2), int(box["y"] + box["height"] / 2))

    def close(self):
        if self._closed:
            return
        self._closed = True
        try:
            if self._browser is not None:
                self._browser.close()
        except Exception:
            pass
        try:
            if self._playwright is not None:
                self._playwright.stop()
        except Exception:
            pass
        try:
            if self._ws_server is not None:
                self._ws_server.shutdown()
        except Exception:
            pass
        if getattr(self, "_reader_stop", None) is not None:
            self._reader_stop.set()
        if self.master_fd is not None:
            try:
                os.close(self.master_fd)
            except OSError:
                pass
        self.kill()
        if self.pid:
            try:
                os.waitpid(self.pid, os.WNOHANG)
            except ChildProcessError:
                pass

    # ---- native input/capture (preferred by drive.py over the Orca path) --

    def _focus(self):
        self._page.evaluate("() => window.__term.focus()")

    def type_text(self, text):
        self._focus()
        self._page.keyboard.type(text)

    def press_key(self, name):
        self._focus()
        n = name.lower()
        if n in KEY_MAP:
            self._page.keyboard.press(KEY_MAP[n])
            return
        if "+" in n:
            *mods, base = n.split("+")
            base_key = KEY_MAP.get(base, base.upper() if len(base) == 1 else base.capitalize())
            combo = "+".join(MOD_MAP[m] for m in mods) + "+" + base_key
            self._page.keyboard.press(combo)
            return
        if len(name) == 1:
            self._page.keyboard.type(name)
            return
        raise HeadlessError("unknown key name %r" % name)

    def scroll(self, direction, n):
        # xterm.js's mouse-report wheel handler sends one SGR wheel report
        # per DOM wheel event it receives (it reads only deltaY's sign, not
        # its magnitude, when kiln's mouse tracking is on — checked against
        # node_modules/@xterm/xterm/lib/xterm.js's "wheel" case), so "n
        # notches" means n discrete wheel events, not one call with an n
        # times larger delta (a single such call still reports as one
        # notch, under-scrolling every SCROLL step that asks for more than
        # one).
        self._focus()
        box = self._page.locator("#terminal").bounding_box()
        x, y = box["x"] + box["width"] / 2, box["y"] + box["height"] / 2
        self._page.mouse.move(x, y)
        delta = 40 * (1 if direction == "down" else -1)
        for _ in range(int(n)):
            self._page.mouse.wheel(0, delta)
            self._page.wait_for_timeout(20)

    def click(self, x, y):
        self._focus()
        box = self._page.locator("#terminal").bounding_box()
        self._page.mouse.click(box["x"] + int(x), box["y"] + int(y))

    def screenshot(self, path):
        self._page.locator("#terminal").screenshot(path=path)

    def cell_fg(self, row, col):
        """The resolved RGB foreground colour of one screen cell (0-indexed,
        row within the visible screen's last `self.rows` scrollback lines),
        read through the xterm.js buffer API. Used to verify a theme's
        tokens actually reached the screen, not just that the right OSC 11
        reply was sent."""
        buf_row = row
        total = self._page.evaluate("() => window.__term.buffer.active.length")
        buf_row = total - self.rows + row
        return self._page.evaluate(
            "([r, c]) => window.__cellFg(r, c)", [buf_row, col])

    # ---- process tracking ---------------------------------------------------

    def alive(self):
        if not self.pid:
            return False
        try:
            os.kill(self.pid, 0)
        except ProcessLookupError:
            return False
        except PermissionError:
            return True
        return True

    def child_pid(self):
        return self.pid if self.alive() else None

    def kill(self):
        if self.pid:
            try:
                os.kill(self.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
