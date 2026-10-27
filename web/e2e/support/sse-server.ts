import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";

/**
 * A real streaming SSE endpoint for the assistant e2e tests. page.route can
 * only fulfill a whole body at once, so /ask requests are continued to this
 * local server, which writes each frame in two chunks with delays, the way
 * the Go agent flushes one event at a time.
 */

export interface SseFrame {
  event: string;
  data: unknown;
  /** Wait before writing this frame, ms. */
  delay?: number;
}

export type AskBody = {
  question: string;
  conversationId?: string;
  assumptionOverrides?: { label: string; sqlFragment: string }[];
  clarification?: { question: string; answer: string; saveToGlossary?: boolean };
  confirmCost?: boolean;
  confirmRows?: boolean;
};

export type AskScript = (body: AskBody, call: number) => SseFrame[];

interface Registered {
  script: AskScript;
  calls: AskBody[];
}

const scripts = new Map<string, Registered>();
let server: Promise<{ server: Server; port: number }> | null = null;
let nextId = 1;

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    let raw = "";
    req.setEncoding("utf8");
    req.on("data", (chunk: string) => {
      raw += chunk;
    });
    req.on("end", () => resolve(raw));
    req.on("error", reject);
  });
}

function start(): Promise<{ server: Server; port: number }> {
  return new Promise((resolve) => {
    const s = createServer(async (req, res) => {
      const id = req.url?.split("/").pop() ?? "";
      const entry = scripts.get(id);
      if (!entry) {
        res.writeHead(404).end();
        return;
      }
      const body = JSON.parse((await readBody(req)) || "{}") as AskBody;
      entry.calls.push(body);
      const frames = entry.script(body, entry.calls.length);
      let closed = false;
      res.on("close", () => {
        closed = true;
      });
      res.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        "Access-Control-Allow-Origin": "*",
      });
      res.write(": stream open\n\n");
      for (const frame of frames) {
        if (frame.delay) await sleep(frame.delay);
        if (closed) return;
        const text = `event: ${frame.event}\ndata: ${JSON.stringify(frame.data)}\n\n`;
        const cut = Math.max(1, Math.floor(text.length / 2));
        res.write(text.slice(0, cut));
        await sleep(5);
        if (closed) return;
        res.write(text.slice(cut));
      }
      res.end();
    });
    s.listen(0, "127.0.0.1", () => resolve({ server: s, port: (s.address() as AddressInfo).port }));
  });
}

/** Registers a script; returns the URL /ask should continue to and the bodies it received. */
export async function registerAskScript(script: AskScript): Promise<{ url: string; calls: AskBody[] }> {
  server ??= start();
  const { port } = await server;
  const id = `s${process.pid}-${nextId++}`;
  const calls: AskBody[] = [];
  scripts.set(id, { script, calls });
  return { url: `http://127.0.0.1:${port}/ask/${id}`, calls };
}
