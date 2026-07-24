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
  const statusText = status.querySelector(".status-text");
  const uploadButton = document.getElementById("uploadButton");
  const fileInput = document.getElementById("fileInput");
  const dropzone = document.getElementById("dropzone");
  const encoder = new TextEncoder();
  const decoder = new TextDecoder();
  const MAX_UPLOAD = 64 * 1024 * 1024;
  let socket = null;
  let reconnectTimer = null;

  terminal.loadAddon(fitAddon);
  terminal.open(document.getElementById("terminal"));

  function setStatus(text, state) {
    statusText.textContent = text;
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
    setStatus("连接中", "connecting");
    const scheme = location.protocol === "https:" ? "wss:" : "ws:";
    const path = location.pathname.endsWith("/") ? location.pathname : location.pathname + "/";
    socket = new WebSocket(`${scheme}//${location.host}${path}ws`);
    socket.binaryType = "arraybuffer";
    socket.onopen = () => {
      setStatus("已连接", "connected");
      terminal.clear();
      fitAddon.fit();
      sendSize();
      terminal.focus();
      uploadButton.disabled = false;
    };
    socket.onmessage = (event) => {
      const frame = new Uint8Array(event.data);
      if (frame.length < 1) return;
      if (frame[0] === 0) {
        terminal.write(decoder.decode(frame.subarray(1), { stream: true }));
      } else if (frame[0] === 3) {
        handleUploadResult(frame.subarray(1));
      }
    };
    socket.onerror = () => socket.close();
    socket.onclose = () => {
      setStatus("连接断开", "offline");
      uploadButton.disabled = true;
      reconnectTimer = setTimeout(connect, 2000);
    };
  }

  function notify(text) {
    terminal.write(`\r\n\x1b[38;5;80m[anyssh] ${text}\x1b[0m\r\n`);
  }

  function handleUploadResult(bytes) {
    let result;
    try {
      result = JSON.parse(decoder.decode(bytes));
    } catch {
      return;
    }
    if (result.ok) {
      notify(`已上传 ${result.name}（${result.size} 字节）到 ${result.path}`);
    } else {
      notify(`上传失败：${result.message || "未知错误"}`);
    }
    terminal.focus();
  }

  async function uploadFile(file) {
    if (!socket || socket.readyState !== WebSocket.OPEN) return;
    if (file.size > MAX_UPLOAD) {
      notify(`文件超过 ${Math.floor(MAX_UPLOAD / 1024 / 1024)} MiB 上限`);
      return;
    }
    uploadButton.dataset.busy = "true";
    uploadButton.disabled = true;
    try {
      const header = encoder.encode(JSON.stringify({ name: file.name, size: file.size }));
      const content = new Uint8Array(await file.arrayBuffer());
      const frame = new Uint8Array(1 + 4 + header.length + content.length);
      frame[0] = 2;
      new DataView(frame.buffer).setUint32(1, header.length, false);
      frame.set(header, 5);
      frame.set(content, 5 + header.length);
      socket.send(frame);
      notify(`正在上传 ${file.name}…`);
    } catch (error) {
      notify(`读取文件失败：${error.message || error}`);
    } finally {
      delete uploadButton.dataset.busy;
      uploadButton.disabled = !socket || socket.readyState !== WebSocket.OPEN;
    }
  }

  const canUpload = () => socket && socket.readyState === WebSocket.OPEN;

  uploadButton.addEventListener("click", () => fileInput.click());
  fileInput.addEventListener("change", () => {
    const file = fileInput.files && fileInput.files[0];
    if (file) uploadFile(file);
    fileInput.value = "";
  });

  let dragDepth = 0;
  window.addEventListener("dragenter", (event) => {
    event.preventDefault();
    if (!canUpload()) return;
    dragDepth += 1;
    dropzone.classList.add("show");
  });
  window.addEventListener("dragover", (event) => event.preventDefault());
  window.addEventListener("dragleave", (event) => {
    event.preventDefault();
    dragDepth -= 1;
    if (dragDepth <= 0) {
      dragDepth = 0;
      dropzone.classList.remove("show");
    }
  });
  window.addEventListener("drop", (event) => {
    event.preventDefault();
    dragDepth = 0;
    dropzone.classList.remove("show");
    const file = event.dataTransfer && event.dataTransfer.files && event.dataTransfer.files[0];
    if (file && canUpload()) uploadFile(file);
  });

  terminal.onData((data) => sendFrame(0, data));
  terminal.onResize(sendSize);
  new ResizeObserver(() => fitAddon.fit()).observe(document.getElementById("terminal"));
  window.addEventListener("beforeunload", () => clearTimeout(reconnectTimer));
  connect();
})();
