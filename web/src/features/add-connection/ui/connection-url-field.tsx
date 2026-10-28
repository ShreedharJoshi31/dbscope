import { CircleAlert, Link2 } from "lucide-react";
import { type KeyboardEvent, type Ref, useId, useState } from "react";
import { type ParsedConnectionUrl, useParseConnectionUrl } from "@/shared/api";
import { Button } from "@/shared/ui/button";
import { Input } from "@/shared/ui/input";
import { Label } from "@/shared/ui/label";

interface ConnectionUrlFieldProps {
  onParsed: (parsed: ParsedConnectionUrl) => void;
  /** An example URL of the selected engine. */
  placeholder: string;
  inputRef?: Ref<HTMLInputElement>;
}

/**
 * Pasting (or Enter / "Fill fields") sends the URL to the agent's parser and
 * fills the form. The URL is cleared afterwards because it may hold a password.
 */
export function ConnectionUrlField({ onParsed, placeholder, inputRef }: ConnectionUrlFieldProps) {
  const id = useId();
  const [url, setUrl] = useState("");
  const [notes, setNotes] = useState<string[]>([]);
  const parse = useParseConnectionUrl();

  function submit(raw: string) {
    const trimmed = raw.trim();
    if (!trimmed) return;
    parse.mutate(trimmed, {
      onSuccess: (parsed) => {
        setUrl("");
        setNotes(parsed.notes ?? []);
        onParsed(parsed);
      },
    });
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key !== "Enter") return;
    // Enter here parses the URL; it must not submit the surrounding form.
    event.preventDefault();
    submit(url);
  }

  const errorId = `${id}-error`;
  const hintId = `${id}-hint`;

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id} className="text-xs font-medium">
        Paste a connection URL
      </Label>
      <div className="flex gap-2">
        <div className="relative flex-1">
          <Link2
            aria-hidden
            className="pointer-events-none absolute top-1/2 left-2 size-4 -translate-y-1/2 text-text-muted"
          />
          <Input
            id={id}
            ref={inputRef}
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            onPaste={(e) => {
              const pasted = e.clipboardData.getData("text");
              if (!pasted.trim()) return;
              e.preventDefault();
              setUrl(pasted);
              submit(pasted);
            }}
            onKeyDown={onKeyDown}
            placeholder={placeholder}
            autoComplete="off"
            spellCheck={false}
            className="pl-8 font-mono text-xs"
            aria-invalid={parse.isError || undefined}
            aria-describedby={parse.isError ? `${errorId} ${hintId}` : hintId}
          />
        </div>
        <Button type="button" variant="outline" onClick={() => submit(url)} disabled={!url.trim() || parse.isPending}>
          {parse.isPending ? "Reading…" : "Fill fields"}
        </Button>
      </div>
      {parse.isError && (
        <p id={errorId} className="flex items-start gap-1 text-xs text-status-danger-fg">
          <CircleAlert aria-hidden className="mt-0.5 size-3 shrink-0" />
          {parse.error.message}
        </p>
      )}
      {notes.length > 0 && !parse.isError && (
        <ul className="flex flex-col gap-0.5 text-xs text-status-warn-fg">
          {notes.map((note) => (
            <li key={note} className="flex items-start gap-1">
              <CircleAlert aria-hidden className="mt-0.5 size-3 shrink-0" />
              {note}
            </li>
          ))}
        </ul>
      )}
      <p id={hintId} className="text-xs text-text-muted">
        PostgreSQL, MySQL, MongoDB, Redis, Neo4j, ClickHouse and Elasticsearch (http or https) URLs work, and the engine
        follows the URL. It is parsed on this machine and not kept.
      </p>
    </div>
  );
}
