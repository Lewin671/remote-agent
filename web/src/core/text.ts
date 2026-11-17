import * as l10n from "../i18n";
import type { Text } from "../gen/remoteagent/core/v1/text_pb";

/** Resolves a Core-produced localized Text message through web i18n. */
export function renderCoreText(text?: Text): string {
  if (!text) return "";
  if (text.join.length > 0) {
    const sep = text.separatorKey
      ? (l10n.t as (key: string) => string)(text.separatorKey)
      : text.separatorLiteral;
    return text.join.map(renderCoreText).filter(Boolean).join(sep);
  }
  if (!text.key) return text.literal;
  const params: Record<string, string | number> = {};
  for (const arg of text.args) {
    switch (arg.value.case) {
      case "strValue":
        params[arg.name] = arg.value.value;
        break;
      case "intValue":
        params[arg.name] = Number(arg.value.value);
        break;
      case "floatValue":
        params[arg.name] = arg.value.value;
        break;
      case "textValue":
        params[arg.name] = renderCoreText(arg.value.value);
        break;
    }
  }
  return (l10n.t as (key: string, params?: Record<string, string | number>) => string)(text.key, params);
}
