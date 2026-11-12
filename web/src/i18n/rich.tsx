import { Fragment, type ReactNode } from "react";

/** Replace named display slots after localization, preserving markup and order. */
export function rich(text: string, slots: Record<string, ReactNode>): ReactNode {
  return text.split(/(\{[a-zA-Z]+\})/).map((part, index) =>
    <Fragment key={index}>{part.startsWith("{") && part.endsWith("}") && part.slice(1, -1) in slots ? slots[part.slice(1, -1)] : part}</Fragment>,
  );
}
