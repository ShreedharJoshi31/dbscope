export interface SseEvent {
  event: string;
  data: string;
  id?: string;
}

function abortError(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException("The stream was aborted.", "AbortError");
}

/**
 * Parses a `text/event-stream` body per the WHATWG SSE rules: frames end at a
 * blank line, `data:` lines join with "\n", `:` lines are comments, CR, LF and
 * CRLF all end a line, and frames or UTF-8 sequences may split across chunks.
 * Aborting the signal cancels the underlying stream and rejects with the reason.
 */
export async function* readSseStream(
  stream: ReadableStream<Uint8Array>,
  signal?: AbortSignal,
): AsyncGenerator<SseEvent, void, undefined> {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  const onAbort = () => {
    reader.cancel(signal?.reason).catch(() => {});
  };
  signal?.addEventListener("abort", onAbort, { once: true });

  let buffer = "";
  let eventType = "";
  let dataLines: string[] = [];
  let lastId: string | undefined;

  function processLine(line: string): SseEvent | null {
    if (line === "") {
      if (dataLines.length === 0) {
        eventType = "";
        return null;
      }
      const frame: SseEvent = { event: eventType || "message", data: dataLines.join("\n") };
      if (lastId !== undefined) frame.id = lastId;
      eventType = "";
      dataLines = [];
      return frame;
    }
    if (line.startsWith(":")) return null;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") eventType = value;
    else if (field === "data") dataLines.push(value);
    else if (field === "id" && !value.includes("\0")) lastId = value;
    return null;
  }

  let finished = false;
  try {
    if (signal?.aborted) throw abortError(signal);
    while (true) {
      const { value, done } = await reader.read();
      if (signal?.aborted) throw abortError(signal);
      finished = done;
      buffer += done ? decoder.decode() : decoder.decode(value, { stream: true });

      let start = 0;
      for (let i = 0; i < buffer.length; i++) {
        const ch = buffer[i];
        if (ch !== "\n" && ch !== "\r") continue;
        // A trailing CR may be the first half of a CRLF split across chunks.
        if (ch === "\r" && i === buffer.length - 1 && !done) break;
        const frame = processLine(buffer.slice(start, i));
        if (ch === "\r" && buffer[i + 1] === "\n") i++;
        start = i + 1;
        if (frame) yield frame;
      }
      buffer = buffer.slice(start);

      if (done) return;
    }
  } finally {
    signal?.removeEventListener("abort", onAbort);
    // The consumer stopped early or an error was thrown: close the connection.
    if (!finished) reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}
