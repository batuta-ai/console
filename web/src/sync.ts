import { T } from "./i18n";

export const READ_INTERVAL = 4000;
export const REQUEST_DEADLINE = 10000;

// Race the whole operation, including response decoding and receipt hashing.
// Callers still guard commits: cancellation cannot retract a submitted mutation.
export async function bounded<T>(work: (signal: AbortSignal) => Promise<T>, parent?: AbortSignal): Promise<T> {
  const controller = new AbortController();
  const abort = () => controller.abort(parent?.reason);
  parent?.addEventListener("abort", abort, { once: true });
  if (parent?.aborted) abort();
  const timer = window.setTimeout(() => controller.abort(new Error(T("sync_timeout"))), REQUEST_DEADLINE);
  let rejectAbort: () => void = () => {};
  const canceled = new Promise<never>((_, reject) => {
    rejectAbort = () => reject(controller.signal.reason);
    controller.signal.addEventListener("abort", rejectAbort, { once: true });
    if (controller.signal.aborted) rejectAbort();
  });
  try {
    controller.signal.throwIfAborted();
    return await Promise.race([work(controller.signal), canceled]);
  } finally {
    window.clearTimeout(timer);
    parent?.removeEventListener("abort", abort);
    controller.signal.removeEventListener("abort", rejectAbort);
    controller.abort();
  }
}

export function followEvents(refresh: () => void, failed: (message: string) => void) {
  let stopped = false;
  let socket: WebSocket | undefined;
  let reconnect = 0;
  let debounce = 0;
  let handshake = 0;
  let delay = 1000;
  let cursor: number | undefined;
  const scheduleRefresh = () => {
    if (debounce || stopped) return;
    debounce = window.setTimeout(() => { debounce = 0; refresh(); }, 200);
  };
  const connect = () => {
    if (stopped) return;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const token = localStorage.getItem("bt-token") || "";
    const sock = new WebSocket(`${proto}://${location.host}/v0/events?token=${encodeURIComponent(token)}${cursor === undefined ? "" : `&cursor=${cursor}`}`);
    socket = sock;
    let ended = false;
    const lost = () => {
      if (ended || stopped) return;
      ended = true;
      window.clearTimeout(handshake);
      sock.onopen = sock.onmessage = sock.onclose = sock.onerror = null;
      sock.close();
      failed(T("sync_reconnecting"));
      reconnect = window.setTimeout(connect, delay);
      delay = Math.min(delay * 2, 30000);
    };
    handshake = window.setTimeout(lost, REQUEST_DEADLINE);
    sock.onopen = () => {
      if (ended || stopped) return;
      scheduleRefresh();
    };
    sock.onmessage = event => {
      if (ended || stopped) return;
      try {
        const envelope = JSON.parse(event.data);
        const events = envelope?.type === "snapshot" ? envelope.events : envelope?.type === "event" ? [envelope.event] : undefined;
        if (!Array.isArray(events)) throw new Error(T("sync_invalid"));
        let next = cursor;
        for (const entry of events) {
          if (!Number.isSafeInteger(entry?.seq) || entry.seq < 0 || (next !== undefined && entry.seq <= next)) throw new Error(T("sync_invalid"));
          next = entry.seq;
        }
        cursor = next;
        window.clearTimeout(handshake);
        delay = 1000;
        failed("");
        scheduleRefresh();
      } catch {
        lost();
        failed(T("sync_invalid"));
      }
    };
    sock.onclose = lost;
    sock.onerror = lost;
  };
  connect();
  return () => {
    stopped = true;
    window.clearTimeout(reconnect);
    window.clearTimeout(debounce);
    window.clearTimeout(handshake);
    if (socket) {
      socket.onopen = socket.onmessage = socket.onclose = socket.onerror = null;
      socket.close();
    }
  };
}
