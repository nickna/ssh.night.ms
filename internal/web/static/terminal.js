// terminal.js — wire xterm.js to the /ws/bbs WebSocket. Sends keystrokes
// as binary frames, renders incoming binary as ANSI, and sends resize
// events as JSON text frames so the bubbletea Update loop can dispatch
// WindowSizeMsg.
(function () {
  "use strict";

  const statusEl = document.getElementById("status");
  const termEl = document.getElementById("term");

  // Lightweight palette tuned to match the lipgloss theme used by the TUI.
  const theme = {
    background: "#0e0b16",
    foreground: "#e6e0f2",
    cursor: "#ff7db0",
    cursorAccent: "#0e0b16",
    selectionBackground: "#241d33",
    black: "#241d33",
    red: "#ff6b7a",
    green: "#5ee39c",
    yellow: "#ffd166",
    blue: "#5ee7df",
    magenta: "#ff7db0",
    cyan: "#5ee7df",
    white: "#e6e0f2",
    brightBlack: "#3f3f46",
    brightRed: "#ff6b7a",
    brightGreen: "#5ee39c",
    brightYellow: "#ffd166",
    brightBlue: "#5ee7df",
    brightMagenta: "#ff7db0",
    brightCyan: "#5ee7df",
    brightWhite: "#e6e0f2",
  };

  const term = new Terminal({
    theme,
    fontFamily: 'ui-monospace, "SF Mono", "Cascadia Mono", Menlo, monospace',
    fontSize: 14,
    cursorBlink: true,
    convertEol: false,
    scrollback: 5000,
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(termEl);
  fit.fit();

  const wsURL =
    (location.protocol === "https:" ? "wss:" : "ws:") +
    "//" +
    location.host +
    "/ws/bbs";
  const ws = new WebSocket(wsURL, "nightms.bbs.v1");
  ws.binaryType = "arraybuffer";

  function setStatus(text, classes) {
    statusEl.textContent = text;
    statusEl.className = "status " + (classes || "");
  }

  function sendResize() {
    if (ws.readyState !== WebSocket.OPEN) return;
    const dims = { type: "resize", cols: term.cols, rows: term.rows };
    ws.send(JSON.stringify(dims));
  }

  ws.addEventListener("open", () => {
    setStatus("connected", "ok");
    sendResize();
    term.focus();
  });

  ws.addEventListener("message", (ev) => {
    if (typeof ev.data === "string") {
      // Reserved for future server -> client control messages; ignore for now.
      return;
    }
    term.write(new Uint8Array(ev.data));
  });

  ws.addEventListener("close", (ev) => {
    setStatus("disconnected (" + ev.code + ")", "err");
    term.write("\r\n\x1b[31m[session ended]\x1b[0m\r\n");
  });

  ws.addEventListener("error", () => {
    setStatus("connection error", "err");
  });

  // xterm gives us raw bytes as the user types — already in the encoding
  // bubbletea expects (escape sequences for arrows, etc.). Forward verbatim.
  term.onData((data) => {
    if (ws.readyState !== WebSocket.OPEN) return;
    ws.send(new TextEncoder().encode(data));
  });

  // Resize follows window changes AND user-driven resize of the terminal
  // box (CSS resize: both on .terminal). A single debounce shared between
  // the two sources keeps a slow drag from spamming WS frames. The
  // ResizeObserver also covers font-load reflow and devtools toggles.
  let resizeTimer = null;
  function refit() {
    if (resizeTimer) clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      fit.fit();
      sendResize();
    }, 80);
  }
  window.addEventListener("resize", refit);
  new ResizeObserver(refit).observe(termEl);
})();
