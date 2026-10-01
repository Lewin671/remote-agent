import { createContext, useContext, useState, type ComponentProps, type ReactNode } from "react";

const RowStateContext = createContext<{ key: string; states: Map<string, boolean> } | undefined>(undefined);

/** Expansion belongs to a chat row, not its temporary viewport mount. */
export function RowState({ id, states, children }: { id: string; states: Map<string, boolean>; children: ReactNode }) {
  return <RowStateContext.Provider value={{ key: id, states }}>{children}</RowStateContext.Provider>;
}

export function useRowExpanded(name: string): [boolean, (value: boolean) => void] {
  const ctx = useContext(RowStateContext);
  const key = ctx ? `${ctx.key}/${name}` : "";
  const [open, setOpen] = useState(() => ctx?.states.get(key) ?? false);
  return [open, (value) => {
    if (ctx) { if (value) ctx.states.set(key, true); else ctx.states.delete(key); }
    setOpen(value);
  }];
}

export function RememberDetails({ name, ...props }: ComponentProps<"details"> & { name: string }) {
  const [open, setOpen] = useRowExpanded(name);
  return <details {...props} open={open} onToggle={(e) => setOpen(e.currentTarget.open)} />;
}
