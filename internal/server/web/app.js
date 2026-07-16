(() => {
  "use strict";

  const terminal = new Terminal({
    cursorBlink: true,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
    fontSize: 14,
    scrollback: 5000,
    theme: {
      background: "#111315",
      foreground: "#e8e9e8",
      cursor: "#63d4a3",
      selectionBackground: "#42515e"
    }
  });
  const fitAddon = new FitAddon.FitAddon();
  const status = document.getElementById("status");
  const encoder = new TextEncoder();
  const decoder = new TextDecoder();
  let socket = null;
  let reconnectTimer = null;

  terminal.loadAddon(fitAddon);
  terminal.open(document.getElementById("terminal"));

  function setStatus(text, state) {
    status.textContent = text;
    status.dataset.state = state;
  }

  function sendFrame(type, payload) {
    if (!socket || socket.readyState !== WebSocket.OPEN) return;
    const body = typeof payload === "string" ? encoder.encode(payload) : payload;
    const frame = new Uint8Array(body.length + 1);
    frame[0] = type;
    frame.set(body, 1);
    socket.send(frame);
  }

  function sendSize() {
    sendFrame(1, encoder.encode(JSON.stringify({ cols: terminal.cols, rows: terminal.rows })));
  }

  function connect() {
    clearTimeout(reconnectTimer);
    setStatus("Connecting", "connecting");
    const scheme = location.protocol === "https:" ? "wss:" : "ws:";
    const path = location.pathname.endsWith("/") ? location.pathname : location.pathname + "/";
    socket = new WebSocket(`${scheme}//${location.host}${path}ws`);
    socket.binaryType = "arraybuffer";
    socket.onopen = () => {
      setStatus("Connected", "connected");
      terminal.clear();
      fitAddon.fit();
      sendSize();
      terminal.focus();
    };
    socket.onmessage = (event) => {
      const frame = new Uint8Array(event.data);
      if (frame.length > 1 && frame[0] === 0) {
        terminal.write(decoder.decode(frame.subarray(1), { stream: true }));
      }
    };
    socket.onerror = () => socket.close();
    socket.onclose = () => {
      setStatus("Disconnected", "offline");
      reconnectTimer = setTimeout(connect, 2000);
    };
  }

  terminal.onData((data) => sendFrame(0, data));
  terminal.onResize(sendSize);
  new ResizeObserver(() => fitAddon.fit()).observe(document.getElementById("terminal"));
  window.addEventListener("beforeunload", () => clearTimeout(reconnectTimer));
  connect();
})();
