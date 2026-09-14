// Temporary same-origin browser test observer, not production UI code.
// Tee only this run's three fixture responses. Never inspect cookies or tokens.
function installBetaBodyProbe(directory) {
  if (location.origin !== "https://beta-executor-dashboard.0ruka.dev" ||
      !/^\/private\/tmp\/executor-beta-final-[a-zA-Z0-9_-]+$/.test(directory)) {
    throw new Error("Unexpected test origin or directory");
  }
  const original = window.fetch;
  const counts = {}, results = {}, pending = new Set();
  const names = new Set(["wait.fifo", "normal.txt", "empty.txt"]);
  const wrapper = async function(input, init) {
    let request;
    try { request = JSON.parse(init?.body); } catch { /* Not our control request. */ }
    const path = request?.arguments?.path;
    const name = typeof path === "string" && path.startsWith(directory + "/") ? path.slice(directory.length + 1) : "";
    const url = typeof input === "string" ? new URL(input, location.origin) : null;
    if (!url || url.origin !== location.origin ||
        url.pathname !== "/api/devices/device-f8WsfPqGN5f8vbVTSFGQS49l4eXn8lqV/call" ||
        request?.method !== "filesystem_read" || request.arguments.action !== "read_file" || !names.has(name)) {
      return original.call(window, input, init);
    }
    counts[name] = (counts[name] || 0) + 1;
    const controller = new AbortController();
    pending.add(controller);
    const timer = setTimeout(() => controller.abort(), 20000);
    const signal = init.signal ? AbortSignal.any([init.signal, controller.signal]) : controller.signal;
    try {
      const response = await original.call(window, input, { ...init, signal });
      const copy = response.clone();
      // Read the network response independently of the UI's intentional cancellation.
      (async () => {
        let bytes = 0, eof = false;
        const chunks = [];
        let reader;
        try {
          reader = copy.body?.getReader();
          if (!reader) throw new Error("Missing response body");
          for (;;) {
            const item = await reader.read();
            if (item.done) { eof = true; break; }
            bytes += item.value.byteLength;
            if (bytes > 8192) throw new Error("Bound exceeded");
            chunks.push(item.value);
          }
          const data = new Uint8Array(bytes);
          let offset = 0;
          for (const chunk of chunks) { data.set(chunk, offset); offset += chunk.byteLength; }
          const json = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(data));
          const headerID = response.headers.get("x-executor-request-id");
          const value = { status: response.status, content_type: response.headers.get("content-type"),
            body_bytes: bytes, normal_eof: eof, read_exception: false, utf8_json_valid: true,
            request_id_header: headerID };
          if (name === "wait.fifo") {
            Object.assign(value, { code: json.code, request_id: json.request_id, outcome: json.outcome,
              accepted: response.status === 502 && value.content_type?.startsWith("application/json") &&
                bytes > 0 && eof && json.code === "relay_stream_failed" &&
                typeof headerID === "string" && json.request_id === headerID && json.outcome === "unconfirmed" });
          } else {
            const content = json.payload?.result?.content;
            const expected = name === "empty.txt" ? "" : "Executor Beta finalization 正常讀取 ✅\n";
            const decoded = request.arguments.encoding === "base64" ?
              new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.from(atob(content), c => c.charCodeAt(0))) : content;
            Object.assign(value, { content_matches: decoded === expected, accepted: response.status === 200 && decoded === expected });
          }
          results[name] = value;
        } catch (error) {
          results[name] = { accepted: false, body_bytes: bytes, normal_eof: eof,
            read_exception: true, exception_type: error.name };
          controller.abort();
        } finally {
          clearTimeout(timer);
          pending.delete(controller);
          reader?.releaseLock();
        }
      })();
      return response;
    } catch (error) {
      clearTimeout(timer);
      pending.delete(controller);
      results[name] = { accepted: false, fetch_exception: error.name };
      throw error;
    }
  };
  window.fetch = wrapper;
  return {
    snapshot: () => ({ counts: { ...counts }, results: { ...results }, pending: pending.size }),
    cleanup: () => {
      if (window.fetch !== wrapper) throw new Error("Fetch changed outside this test");
      window.fetch = original;
      for (const controller of pending) controller.abort();
      return { restored: true, pending_at_cleanup: pending.size };
    },
  };
}
