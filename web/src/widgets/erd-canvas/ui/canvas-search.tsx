import { Search, X } from "lucide-react";
import type { KeyboardEvent, Ref } from "react";
import { useCanvasStore } from "@/entities/schema";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { announce } from "@/shared/lib/announcer";
import { Kbd } from "@/shared/ui/kbd";

interface CanvasSearchProps {
  /** Where focus goes after Esc, so the user is back on the canvas. */
  onExit: () => void;
  ref?: Ref<HTMLInputElement>;
}

/** Filters and highlights tables and columns; Enter cycles matches and pans to each. */
export function CanvasSearch({ onExit, ref }: CanvasSearchProps) {
  const query = useCanvasStore((s) => s.search.query);
  const count = useCanvasStore((s) => s.search.matches.length);
  const index = useCanvasStore((s) => s.search.index);

  const step = (direction: 1 | -1) => {
    const match = useCanvasStore.getState().stepMatch(direction);
    if (!match) {
      announce(query ? `No tables or columns match ${query}.` : "Type a table or column name.");
      return;
    }
    const { search } = useCanvasStore.getState();
    announce(
      `Match ${search.index + 1} of ${search.matches.length}: ${match.nodeId}${match.column ? `.${match.column}` : ""}`,
    );
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter") {
      event.preventDefault();
      step(event.shiftKey ? -1 : 1);
    } else if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      useCanvasStore.getState().clearSearch();
      onExit();
    }
  };

  const status = query
    ? count === 0
      ? "No matches"
      : index >= 0
        ? `${index + 1} of ${count}`
        : `${count} matches`
    : "";

  return (
    <div className="relative flex h-7 w-56 shrink-0 items-center rounded-md border border-border-control bg-surface-canvas focus-within:ring-2 focus-within:ring-border-focus">
      <Search aria-hidden className="ml-2 size-3.5 shrink-0 text-text-muted" />
      <input
        ref={ref}
        type="search"
        value={query}
        onChange={(e) => useCanvasStore.getState().setSearchQuery(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder="Find table or column"
        aria-label="Search tables and columns"
        aria-describedby="erd-search-status"
        aria-keyshortcuts="/"
        className="h-full min-w-0 flex-1 bg-transparent px-2 text-xs text-text-primary outline-none placeholder:text-text-muted [&::-webkit-search-cancel-button]:hidden"
      />
      <span id="erd-search-status" className="shrink-0 pr-1 font-mono text-2xs text-text-muted tabular-nums">
        {status}
      </span>
      {query ? (
        <button
          type="button"
          aria-label="Clear search"
          className="mr-1 flex size-5 shrink-0 items-center justify-center rounded-sm text-text-muted hover:bg-surface-hover hover:text-text-primary"
          onClick={() => useCanvasStore.getState().clearSearch()}
        >
          <X aria-hidden className="size-3" />
        </button>
      ) : (
        <Kbd className="mr-1 font-mono">{SHORTCUTS.canvasSearch.combo.key}</Kbd>
      )}
    </div>
  );
}
