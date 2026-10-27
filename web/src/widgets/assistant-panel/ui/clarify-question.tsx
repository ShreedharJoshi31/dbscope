import { HelpCircle } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "@/shared/ui/button";
import { Checkbox } from "@/shared/ui/checkbox";
import { Input } from "@/shared/ui/input";

interface ClarifyQuestionProps {
  question: string;
  options: readonly string[];
  disabled: boolean;
  onAnswer: (answer: string, remember: boolean) => void;
}

/** The assistant's one clarifying question: option buttons, "Other…" free text and "Remember this answer". */
export function ClarifyQuestion({ question, options, disabled, onAnswer }: ClarifyQuestionProps) {
  const [other, setOther] = useState<string | null>(null);
  // Docs/NL.md: answers are saved to the glossary so the question is not asked again; the user can opt out.
  const [remember, setRemember] = useState(true);
  const id = useId();
  return (
    <section
      data-section="clarify"
      aria-labelledby={`${id}-q`}
      className="flex flex-col gap-2 rounded-md border border-status-info-fg bg-surface-canvas p-2"
    >
      <p id={`${id}-q`} className="flex items-start gap-1.5 text-sm font-medium">
        <HelpCircle aria-hidden className="mt-0.5 size-4 shrink-0 text-status-info-fg" />
        <span className="whitespace-pre-wrap">{question}</span>
      </p>
      <fieldset className="m-0 flex flex-wrap gap-1 border-0 p-0">
        <legend className="sr-only">Answers</legend>
        {options.map((option) => (
          <Button
            key={option}
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() => onAnswer(option, remember)}
          >
            {option}
          </Button>
        ))}
        <Button
          variant="ghost"
          size="sm"
          disabled={disabled}
          aria-expanded={other !== null}
          onClick={() => setOther("")}
        >
          Other…
        </Button>
      </fieldset>
      {other !== null && (
        <form
          className="flex gap-1"
          onSubmit={(e) => {
            e.preventDefault();
            if (other.trim()) onAnswer(other.trim(), remember);
          }}
        >
          <label htmlFor={`${id}-other`} className="sr-only">
            Your answer
          </label>
          <Input
            id={`${id}-other`}
            value={other}
            autoFocus
            onChange={(e) => setOther(e.target.value)}
            placeholder="Your answer"
            className="h-7 text-xs"
          />
          <Button type="submit" size="sm" disabled={disabled || !other.trim()}>
            Answer
          </Button>
        </form>
      )}
      <label htmlFor={`${id}-remember`} className="flex items-center gap-1.5 text-xs text-text-muted">
        <Checkbox id={`${id}-remember`} checked={remember} onCheckedChange={setRemember} />
        Remember this answer in the glossary
      </label>
    </section>
  );
}
