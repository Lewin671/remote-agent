import Foundation
import RAProto

/// Renders a core `PBCoreText` message tree into a localized display string.
public func renderCoreText(
    _ text: PBCoreText?,
    locale: String = MessageLocale.current,
    timeZone: TimeZone = .current
) -> String {
    guard let text else { return "" }
    return renderCoreText(text, locale: locale, timeZone: timeZone)
}

/// Renders a core `PBCoreText` message tree into a localized display string.
public func renderCoreText(
    _ text: PBCoreText,
    locale: String = MessageLocale.current,
    timeZone: TimeZone = .current
) -> String {
    if !text.join.isEmpty {
        let separator = text.separatorKey.isEmpty
            ? text.separatorLiteral
            : L10nText.resolve(key: text.separatorKey, locale: locale)
        return text.join
            .map { renderCoreText($0, locale: locale, timeZone: timeZone) }
            .filter { !$0.isEmpty }
            .joined(separator: separator)
    }
    if text.hasTimestamp {
        return formatCoreTimestamp(text.timestamp, locale: locale, timeZone: timeZone)
    }
    if !text.key.isEmpty {
        var resolvedArgs: [String: L10nTextValue] = [:]
        resolvedArgs.reserveCapacity(text.args.count)
        for arg in text.args {
            switch arg.value {
            case .strValue(let value):
                resolvedArgs[arg.name] = .string(value)
            case .intValue(let value):
                resolvedArgs[arg.name] = .integer(value)
            case .floatValue(let value):
                resolvedArgs[arg.name] = .number(value)
            case .textValue(let nested):
                resolvedArgs[arg.name] = .string(renderCoreText(nested, locale: locale, timeZone: timeZone))
            case .timestampValue(let ts):
                resolvedArgs[arg.name] = .string(formatCoreTimestamp(ts, locale: locale, timeZone: timeZone))
            case nil:
                break
            }
        }
        return L10nText.resolve(key: text.key, args: resolvedArgs, locale: locale)
    }
    return text.literal
}

public func formatCoreTimestamp(
    _ ts: PBCoreTimestampArg,
    locale: String = MessageLocale.current,
    timeZone: TimeZone = .current
) -> String {
    let resolvedLocale = MessageLocale.resolve(locale, system: [])
    let tag = MessageLocale.appleTags[resolvedLocale] ?? resolvedLocale
    let formatter = DateFormatter()
    formatter.locale = Locale(identifier: tag)
    formatter.timeZone = timeZone
    switch ts.style {
    case .time:
        formatter.dateStyle = .none
        formatter.timeStyle = .short
    case .weekday:
        formatter.setLocalizedDateFormatFromTemplate("EEEE")
    case .monthDay:
        formatter.setLocalizedDateFormatFromTemplate("MMMd")
    case .monthDayTime:
        formatter.setLocalizedDateFormatFromTemplate("MMMdjm")
    case .date:
        formatter.dateStyle = .medium
        formatter.timeStyle = .none
    case .dateTime, .unspecified, .UNRECOGNIZED:
        formatter.dateStyle = .medium
        formatter.timeStyle = .short
    }
    return formatter.string(from: Date(timeIntervalSince1970: Double(ts.unixMs) / 1000))
}

public extension PBCoreText {
    func rendered(locale: String = MessageLocale.current, timeZone: TimeZone = .current) -> String {
        renderCoreText(self, locale: locale, timeZone: timeZone)
    }
}
