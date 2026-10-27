import { createContext, useContext } from "react";

export interface TableNodeActions {
  toggleCollapsed: (nodeId: string) => void;
}

/** Provided by the canvas, so node data stays plain and stable. */
export const TableNodeActionsContext = createContext<TableNodeActions>({ toggleCollapsed: () => {} });

export function useTableNodeActions(): TableNodeActions {
  return useContext(TableNodeActionsContext);
}
