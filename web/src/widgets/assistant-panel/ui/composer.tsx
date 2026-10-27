import { ArrowUp, Square } from "lucide-react";
import { type KeyboardEvent, useEffect, useId, useRef, useState } from "react";
import { useThreadStore } from "@/entities/assistant-turn";
import { Button } from "@/shared/ui/button";

const MAX_QUESTION = 2000;

interface ComposerProps {
  disabled: boolean;
  disabledReason?: string | undefined;
  streaming: boolean;
  onSend: (question: string) => void;
  onStop: () => void;
}

/** Enter sends, Shift+Enter adds a line; Stop aborts the stream. */
export function Composer({ disabled, disabledReason, streaming, onSend, onStop }: ComposerProps) {
  const [text, setText] = useState("");
  const ref = useRef<HTMLTextAreaElement>(null);
  const focusRequest = useThreadStore((s) => s.composerFocus);
  const hintId = useId();

  useEffect(() => {
    if (focusRequest > 0) ref.current?.focus();
  }, [focusRequest]);

  const send = () => {
    const question = text.trim();
    if (!question || disabled || streaming || question.length > MAX_QUESTION) return;
    onSend(question);
    setText("");
  };

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    send();
  };

  const over = text.length > MAX_QUESTION;
  return (
    <form
      className="flex shrink-0 flex-col gap-1 border-t border-border-default p-2"
      onSubmit={(e) => {
        e.preventDefault();
        send();
      }}
    >
      <label htmlFor={`${hintId}-q`} className="sr-only">
        Ask about your data
      </label>
      <div className="flex items-end gap-2 rounded-md border border-border-control bg-surface-canvas p-1.5 focus-within:ring-2 focus-within:ring-border-focus">
        <textarea
          ref={ref}
          id={`${hintId}-q`}
          data-testid="assistant-composer"
          rows={2}
          value={text}
          disabled={disabled}
          aria-describedby={hintId}
          aria-invalid={over || undefined}
          placeholder={disabled ? (disabledReason ?? "") : "Ask a question about this database…"}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          className="max-h-40 min-h-10 flex-1 resize-none bg-transparent text-sm text-text-primary outline-none placeholder:text-text-muted"
        />
        {streaming ? (
          <Button type="button" size="icon-sm" variant="outline" aria-label="Stop the answer" onClick={onStop}>
            <Square aria-hidden />
          </Button>
        ) : (
          <Button type="submit" size="icon-sm" aria-label="Send question" disabled={disabled || !text.trim() || over}>
            <ArrowUp aria-hidden />
          </Button>
        )}
      </div>
      <p id={hintId} className="flex justify-between text-2xs text-text-muted">
        <span>Enter sends · Shift+Enter adds a line</span>
        {text.length > MAX_QUESTION * 0.8 && (
          <span className={over ? "text-status-danger-fg" : undefined}>
            {text.length}/{MAX_QUESTION}
          </span>
        )}
      </p>
    </form>
  );
}
