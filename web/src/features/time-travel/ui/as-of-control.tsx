import { cn } from "cn";
import { History, LoaderCircle, X } from "lucide-react";
import { useId, useMemo, useRef, useState } from "react";
import { TIME_TRAVEL_KIND_LABEL } from "@/entities/schema";
import {
  type AsOfTableRow,
  clamp,
  formatAsOf,
  fromAsOf,
  localTimeZone,
  oldestLabel,
  outOfRange,
  pickTables,
  toAsOf,
} from "@/features/time-travel/model/as-of";
import { type Model, type Profile, useTimeTravelBounds } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { Button } from "@/shared/ui/button";
import { Input } from "@/shared/ui/input";
import { Label } from "@/shared/ui/label";
import { Popover, PopoverContent, PopoverDescription, PopoverTitle, PopoverTrigger } from "@/shared/ui/popover";

interface AsOfControlProps {
  profile: Profile;
  model: Model | undefined;
  sql: string;
  /** The tab's applied "as of" instant (RFC 3339 UTC). */
  value: string | undefined;
  onChange: (asOf: string | undefined) => void;
}

function availability(row: AsOfTableRow): string {
  if (row.available) return row.note ? `Available. ${row.note}` : "Available";
  return row.note ?? (row.kind ? "Not available to this user" : "Not versioned");
}

function TableList({ rows, matched, now, tz }: { rows: AsOfTableRow[]; matched: boolean; now: string; tz: string }) {
  return (
    <div className="max-h-48 overflow-y-auto border-t border-border-default pt-2">
      <table data-testid="as-of-tables" className="w-full border-collapse text-left text-xs">
        <caption className="pb-1 text-left text-text-muted">
          {matched ? "Tables this query names" : "Versioned tables in this schema (the query names none of them)"}
        </caption>
        <thead className="text-text-muted">
          <tr>
            <th scope="col" className="pr-2 font-medium">
              Table
            </th>
            <th scope="col" className="pr-2 font-medium">
              Kind
            </th>
            <th scope="col" className="pr-2 font-medium">
              Readable
            </th>
            <th scope="col" className="font-medium">
              Oldest readable time
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.table} data-table={row.table} className="align-top">
              <td className="py-0.5 pr-2 font-mono">{row.table}</td>
              <td className="py-0.5 pr-2">{row.kind ? TIME_TRAVEL_KIND_LABEL[row.kind] : "None"}</td>
              <td className={row.available ? "py-0.5 pr-2" : "py-0.5 pr-2 text-status-warn-fg"}>{availability(row)}</td>
              <td className="py-0.5 font-mono tabular-nums">{oldestLabel(row, now, tz)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function AsOfPicker({
  profile,
  model,
  sql,
  value,
  tz,
  onApply,
}: Omit<AsOfControlProps, "onChange"> & { tz: string; onApply: (asOf: string | undefined) => void }) {
  const picked = useMemo(() => pickTables(sql, model), [sql, model]);
  const bounds = useTimeTravelBounds(profile.id, picked.tables, true);
  const range = useMemo(() => (bounds.data ? clamp(bounds.data, picked.tables) : null), [bounds.data, picked.tables]);
  const [local, setLocal] = useState(() => (value ? fromAsOf(value, tz) : ""));
  const inputId = useId();
  const messageId = useId();

  const asOf = local ? toAsOf(local, tz) : null;
  const side = asOf ? outOfRange(asOf, range) : null;
  const message =
    local && !asOf
      ? "Enter a full date and time."
      : side === "before" && range?.min
        ? `Choose ${formatAsOf(range.min, tz)} or later: some tables cannot be read further back.`
        : side === "after" && range
          ? `Choose ${formatAsOf(range.max, tz)} or earlier: that is the database server's time now.`
          : null;
  const waiting = picked.tables.length > 0 && bounds.isPending;

  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={inputId} className="text-xs">
        Date and time ({tz})
      </Label>
      <div className="flex items-center gap-1">
        <Input
          id={inputId}
          type="datetime-local"
          step={1}
          value={local}
          min={range?.min ? fromAsOf(range.min, tz, "ceil") : undefined}
          max={range ? fromAsOf(range.max, tz) : undefined}
          onChange={(e) => setLocal(e.target.value)}
          aria-invalid={message ? true : undefined}
          aria-describedby={message ? messageId : undefined}
          className="h-7 flex-1 font-mono text-xs"
        />
        <Button
          size="sm"
          variant="outline"
          onClick={() => setLocal(fromAsOf(bounds.data?.now ?? new Date().toISOString(), tz))}
          title="The database server's current time"
        >
          Now
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            setLocal("");
            onApply(undefined);
          }}
        >
          Clear
        </Button>
      </div>
      {message && (
        <p id={messageId} role="alert" className="text-xs text-status-danger-fg">
          {message}
        </p>
      )}
      {picked.tables.length === 0 ? (
        <p className="text-xs text-text-muted">
          No table in the loaded schema can be read as of a past time. The server checks each query when it runs.
        </p>
      ) : bounds.isPending ? (
        <p role="status" className="flex items-center gap-2 text-xs text-text-muted">
          <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
          Reading how far back each table goes…
        </p>
      ) : bounds.isError ? (
        <p role="alert" className="text-xs text-status-danger-fg">
          Could not read how far back the tables go: {bounds.error.message}
        </p>
      ) : (
        <>
          <TableList rows={range?.rows ?? []} matched={picked.matched} now={bounds.data.now} tz={tz} />
          {bounds.data.note && <p className="text-xs text-text-muted">{bounds.data.note}</p>}
        </>
      )}
      <Button
        size="sm"
        className="self-end"
        disabled={!asOf || Boolean(message) || waiting}
        onClick={() => onApply(asOf ?? undefined)}
      >
        Apply
      </Button>
    </div>
  );
}

/**
 * The SQL editor's "As of" control (docs/TIME.md): picks the instant the tab's
 * queries read every table as of. Hidden for engines without time travel; the
 * agent still decides which queries can be read that way.
 */
export function AsOfControl({ profile, model, sql, value, onChange }: AsOfControlProps) {
  const [open, setOpen] = useState(false);
  const tz = useMemo(localTimeZone, []);
  const triggerRef = useRef<HTMLButtonElement>(null);
  if (!profile.capabilities.timeTravel) return null;
  const unsupported = model?.timeTravel?.supported === false;
  const shown = value ? formatAsOf(value, tz) : null;

  const apply = (asOf: string | undefined) => {
    onChange(asOf);
    setOpen(false);
    announce(asOf ? `This tab now reads data as of ${formatAsOf(asOf, tz)}.` : "This tab now reads current data.");
  };

  const popover = (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button
            ref={triggerRef}
            size="sm"
            variant={shown ? "ghost" : "outline"}
            aria-label={shown ? `As of ${shown}, change the time` : "As of: read tables as of a past time"}
          />
        }
      >
        <History aria-hidden />
        {shown ? `As of ${shown}` : "As of"}
      </PopoverTrigger>
      <PopoverContent align="start" className={unsupported ? "w-72" : "w-[30rem]"}>
        <PopoverTitle>Read as of a past time</PopoverTitle>
        {unsupported ? (
          <PopoverDescription data-testid="as-of-unsupported">
            {model?.timeTravel?.note ?? "This server cannot read tables as of a past time."}
          </PopoverDescription>
        ) : (
          <>
            <PopoverDescription>
              Run and Explain read every table as it was at this time. The server refuses a query it cannot read that
              way and says why in Messages.
            </PopoverDescription>
            {open && <AsOfPicker profile={profile} model={model} sql={sql} value={value} tz={tz} onApply={apply} />}
          </>
        )}
      </PopoverContent>
    </Popover>
  );

  // One wrapper either way, so the trigger keeps its node (and focus) when the chip appears.
  return (
    <span
      data-testid={shown ? "as-of-chip" : undefined}
      className={cn("flex items-center", shown && "rounded-md border border-status-info-fg text-status-info-fg")}
    >
      {popover}
      {shown && (
        <Button
          size="icon-xs"
          variant="ghost"
          aria-label="Clear the as of time and read current data"
          title="Read current data"
          onClick={() => {
            apply(undefined);
            triggerRef.current?.focus();
          }}
        >
          <X aria-hidden />
        </Button>
      )}
    </span>
  );
}
