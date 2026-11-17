import { localeStore, t, type Locale, type MessageKey } from "../i18n";
import { TimestampStyle, type Text, type TimestampArg } from "../gen/remoteagent/core/v1/text_pb";

function formatTimestamp(ts?: TimestampArg, locale?: Locale): string {
  if (!ts) return "";
  const loc = locale ?? localeStore.snapshot();
  const date = new Date(Number(ts.unixMs));
  switch (ts.style) {
    case TimestampStyle.TIME:
      return new Intl.DateTimeFormat(loc, { hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
    case TimestampStyle.WEEKDAY:
      return new Intl.DateTimeFormat(loc, { weekday: "short" }).format(date);
    case TimestampStyle.MONTH_DAY:
      return new Intl.DateTimeFormat(loc, { month: "short", day: "numeric" }).format(date);
    case TimestampStyle.MONTH_DAY_TIME:
      return new Intl.DateTimeFormat(loc, {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      }).format(date);
    case TimestampStyle.DATE:
      return new Intl.DateTimeFormat(loc, { year: "numeric", month: "short", day: "numeric" }).format(date);
    case TimestampStyle.DATE_TIME:
    case TimestampStyle.UNSPECIFIED: {
      return new Intl.DateTimeFormat(loc, {
        year: "numeric",
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      }).format(date);
    }
  }
}

/** Renders a core Text protobuf message using the generated Web i18n runtime. */
export function renderText(txt?: Text, locale?: Locale): string {
  if (!txt) return "";
  if (txt.timestamp) return formatTimestamp(txt.timestamp, locale);
  if (txt.join.length > 0) {
    const parts = txt.join.map((part) => renderText(part, locale)).filter(Boolean);
    if (parts.length === 0) return "";
    const sep = txt.separatorKey
      ? t(txt.separatorKey as MessageKey, undefined as never, locale)
      : txt.separatorLiteral;
    return parts.join(sep);
  }
  if (txt.key) {
    const args: Record<string, string | number> = {};
    for (const a of txt.args) {
      switch (a.value.case) {
        case "strValue":
          args[a.name] = a.value.value;
          break;
        case "intValue":
          args[a.name] = Number(a.value.value);
          break;
        case "floatValue":
          args[a.name] = a.value.value;
          break;
        case "textValue":
          args[a.name] = renderText(a.value.value, locale);
          break;
        case "timestampValue":
          args[a.name] = formatTimestamp(a.value.value, locale);
          break;
      }
    }
    return t(txt.key as MessageKey, args as never, locale);
  }
  return txt.literal;
}
