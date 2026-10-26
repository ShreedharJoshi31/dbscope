import { describe, expect, it } from "vitest";
import { readSseStream, type SseEvent } from "@/shared/api";

const encoder = new TextEncoder();

/** A stream that emits the given byte chunks in order, then closes. */
function streamOf(chunks: (string | Uint8Array)[]): { stream: ReadableStream<Uint8Array>; cancelled: () => boolean } {
  let wasCancelled = false;
  let index = 0;
  const stream = new ReadableStream<Uint8Array>({
    pull(controller) {
      const chunk = chunks[index++];
      if (chunk === undefined) controller.close();
      else controller.enqueue(typeof chunk === "string" ? encoder.encode(chunk) : chunk);
    },
    cancel() {
      wasCancelled = true;
    },
  });
  return { stream, cancelled: () => wasCancelled };
}

async function collect(iterable: AsyncIterable<SseEvent>): Promise<SseEvent[]> {
  const out: SseEvent[] = [];
  for await (const event of iterable) out.push(event);
  return out;
}

describe("readSseStream", () => {
  it("parses event and data fields, defaulting the event to message", async () => {
    const { stream } = streamOf(["event: token\ndata: hello\n\ndata: plain\n\n"]);
    await expect(collect(readSseStream(stream))).resolves.toEqual([
      { event: "token", data: "hello" },
      { event: "message", data: "plain" },
    ]);
  });

  it("reassembles frames split at arbitrary byte boundaries", async () => {
    const { stream } = streamOf(["ev", "ent: ro", "w\nda", 'ta: {"a":', "1}\n", "\n", "data: two\n\n"]);
    await expect(collect(readSseStream(stream))).resolves.toEqual([
      { event: "row", data: '{"a":1}' },
      { event: "message", data: "two" },
    ]);
  });

  it("handles CRLF split across chunks and multi-byte UTF-8 split across chunks", async () => {
    const bytes = encoder.encode("data: café ✓\r\n\r\n");
    const splitAt = bytes.indexOf(0xc3) + 1; // inside the two-byte "é"
    const crlf = bytes.length - 3; // between the CR and LF of the first line ending
    const { stream } = streamOf([bytes.slice(0, splitAt), bytes.slice(splitAt, crlf), bytes.slice(crlf)]);
    await expect(collect(readSseStream(stream))).resolves.toEqual([{ event: "message", data: "café ✓" }]);
  });

  it("joins multiple data lines, ignores comments and keeps the last id", async () => {
    const { stream } = streamOf([": keep-alive\n", "id: 7\ndata: line one\ndata:line two\n\n"]);
    await expect(collect(readSseStream(stream))).resolves.toEqual([
      { event: "message", data: "line one\nline two", id: "7" },
    ]);
  });

  it("drops an unterminated frame at end of stream", async () => {
    const { stream } = streamOf(["data: done\n\n", "data: partial"]);
    await expect(collect(readSseStream(stream))).resolves.toEqual([{ event: "message", data: "done" }]);
  });

  it("stops and cancels the stream when the signal aborts", async () => {
    const controller = new AbortController();
    let pulls = 0;
    let cancelled = false;
    const stream = new ReadableStream<Uint8Array>({
      pull(c) {
        pulls++;
        if (pulls === 1) c.enqueue(encoder.encode("data: first\n\n"));
        // Later pulls never resolve, like a server that keeps the stream open.
        return pulls === 1 ? undefined : new Promise(() => {});
      },
      cancel() {
        cancelled = true;
      },
    });

    const seen: SseEvent[] = [];
    const run = (async () => {
      for await (const event of readSseStream(stream, controller.signal)) {
        seen.push(event);
        controller.abort();
      }
    })();

    await expect(run).rejects.toMatchObject({ name: "AbortError" });
    expect(seen).toEqual([{ event: "message", data: "first" }]);
    expect(cancelled).toBe(true);
  });

  it("rejects immediately for an already-aborted signal", async () => {
    const { stream } = streamOf(["data: x\n\n"]);
    const controller = new AbortController();
    controller.abort();
    await expect(collect(readSseStream(stream, controller.signal))).rejects.toMatchObject({ name: "AbortError" });
  });

  it("cancels the stream when the consumer stops early", async () => {
    const { stream, cancelled } = streamOf(["data: a\n\ndata: b\n\n", "data: c\n\n"]);
    for await (const event of readSseStream(stream)) {
      expect(event.data).toBe("a");
      break;
    }
    expect(cancelled()).toBe(true);
  });
});
