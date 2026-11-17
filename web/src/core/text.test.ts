import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { TextArgSchema, TextSchema, TimestampArgSchema, TimestampStyle } from "../gen/remoteagent/core/v1/text_pb";
import { renderText } from "./text";

describe("renderText", () => {
  it("renders literal text and empty values", () => {
    expect(renderText(undefined)).toBe("");
    expect(renderText(create(TextSchema, { literal: "hello" }))).toBe("hello");
  });

  it("renders key+args including nested textValue and plural ints", () => {
    const msg = create(TextSchema, {
      key: "home.waiting_count",
      args: [create(TextArgSchema, { name: "count", value: { case: "intValue", value: 3n } })],
    });
    expect(renderText(msg, "zh-CN")).toBe("3 件事等你");
    expect(renderText(msg, "en")).toBe("3 items need you");

    const nested = create(TextSchema, {
      key: "modes.default_named",
      args: [
        create(TextArgSchema, {
          name: "mode",
          value: { case: "textValue", value: create(TextSchema, { key: "modes.ask" }) },
        }),
      ],
    });
    expect(renderText(nested, "zh-CN")).toBe("默认：询问");
    expect(renderText(nested, "en")).toBe("Default: Ask");
  });

  it("joins non-empty segments with literal or keyed separators", () => {
    const joined = create(TextSchema, {
      join: [
        create(TextSchema, { literal: "alpha" }),
        create(TextSchema, { literal: "" }),
        create(TextSchema, { key: "common.online" }),
      ],
      separatorKey: "common.note_separator",
    });
    expect(renderText(joined, "zh-CN")).toBe("alpha；在线");

    const literalSep = create(TextSchema, {
      join: [
        create(TextSchema, { literal: "a" }),
        create(TextSchema, { literal: "b" }),
      ],
      separatorLiteral: " / ",
    });
    expect(renderText(literalSep)).toBe("a / b");
  });

  it("formats Timestamp and TimestampArg in local timezone for en and zh-CN", () => {
    const sample = BigInt(new Date(2026, 10, 16, 14, 5, 0, 0).getTime());
    const timeText = create(TextSchema, {
      timestamp: create(TimestampArgSchema, { unixMs: sample, style: TimestampStyle.TIME }),
    });
    expect(renderText(timeText, "zh-CN")).toBe("14:05");
    expect(renderText(timeText, "en")).toBe("14:05");

    const dayClock = create(TextSchema, {
      key: "time.day_clock",
      args: [
        create(TextArgSchema, {
          name: "day",
          value: {
            case: "timestampValue",
            value: create(TimestampArgSchema, { unixMs: sample, style: TimestampStyle.WEEKDAY }),
          },
        }),
        create(TextArgSchema, {
          name: "clock",
          value: {
            case: "timestampValue",
            value: create(TimestampArgSchema, { unixMs: sample, style: TimestampStyle.TIME }),
          },
        }),
      ],
    });
    const zhDayClock = renderText(dayClock, "zh-CN");
    const enDayClock = renderText(dayClock, "en");
    expect(zhDayClock).toContain("14:05");
    expect(enDayClock).toContain("14:05");
    expect(zhDayClock).not.toBe(enDayClock);

    for (const style of [
      TimestampStyle.MONTH_DAY,
      TimestampStyle.MONTH_DAY_TIME,
      TimestampStyle.DATE,
      TimestampStyle.DATE_TIME,
    ]) {
      const msg = create(TextSchema, {
        timestamp: create(TimestampArgSchema, { unixMs: sample, style }),
      });
      expect(renderText(msg, "en").length).toBeGreaterThan(0);
      expect(renderText(msg, "zh-CN").length).toBeGreaterThan(0);
    }
  });
});
