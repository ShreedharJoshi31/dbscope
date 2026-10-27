import { cn } from "cn";
import { isNumericType } from "@/entities/query/model/value-format";
import { CellValueView } from "@/entities/query/ui/cell-value";
import type { CellValue, QueryResult, ResultColumn } from "@/shared/api";

interface ResultMiniTableProps {
  result: QueryResult;
  limit?: number;
  caption: string;
}

/** Result columns can repeat a name and rows have no key, so position is the identity. */
const keyed = <T,>(items: readonly T[]) => items.map((item, index) => ({ item, key: `k${index}` }));

function MiniRow({ row, columns }: { row: readonly CellValue[]; columns: readonly ResultColumn[] }) {
  return (
    <tr className="border-b border-border-default last:border-b-0">
      {keyed(row).map(({ item: value, key }, i) => {
        const type = columns[i]?.type;
        const right = isNumericType(type) || typeof value === "number";
        return (
          <td key={key} className={cn("max-w-48 px-2 py-0.5", right && "text-right")}>
            <CellValueView value={value} type={type} />
          </td>
        );
      })}
    </tr>
  );
}

/** A plain table of the first rows, for answer cards. The dock has the full, virtualized grid. */
export function ResultMiniTable({ result, limit = 20, caption }: ResultMiniTableProps) {
  return (
    <div className="max-h-64 overflow-auto rounded-md border border-border-default">
      <table className="w-full border-collapse text-xs">
        <caption className="sr-only">{caption}</caption>
        <thead className="sticky top-0 bg-surface-subtle">
          <tr>
            {keyed(result.columns).map(({ item: c, key }) => (
              <th
                key={key}
                scope="col"
                className={cn(
                  "border-b border-border-default px-2 py-1 font-mono font-semibold whitespace-nowrap",
                  isNumericType(c.type) ? "text-right" : "text-left",
                )}
              >
                {c.name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {keyed(result.rows.slice(0, limit)).map(({ item: row, key }) => (
            <MiniRow key={key} row={row} columns={result.columns} />
          ))}
        </tbody>
      </table>
    </div>
  );
}
