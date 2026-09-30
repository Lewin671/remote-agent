import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { EventSchema, ImageFormat, ImageRefSchema, ImageSource } from "./gen/remoteagent/v1/event_pb";
import { aspectOf, matchesFormat, sessionImages, splitPlaceholders } from "./imageview";

const refs = (...n: number[]) => n.map((cliIndex) => ({ cliIndex }));

describe("placeholders in message text", () => {
  it("drops those before the words and chips those among them", () => {
    expect(splitPlaceholders("[Image #3] [Image #4]这两个页面底部的按钮都点不到", refs(3, 4))).toEqual([{ text: "这两个页面底部的按钮都点不到" }]);
    expect(splitPlaceholders("[Image #1]电脑上，[Image #2]手机上，为什么手机上这么挤？", refs(1, 2))).toEqual([
      { text: "电脑上，" },
      { image: 2 },
      { text: "手机上，为什么手机上这么挤？" },
    ]);
  });

  it("leaves placeholders of images the message does not have as typed", () => {
    expect(splitPlaceholders("看 [Image #9] 这个写法", refs(1))).toEqual([{ text: "看 [Image #9] 这个写法" }]);
    expect(splitPlaceholders("no images", [])).toEqual([{ text: "no images" }]);
  });
});

describe("format checks", () => {
  it("accepts only bytes that are the claimed format", () => {
    const png = Uint8Array.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0]);
    const jpeg = Uint8Array.from([0xff, 0xd8, 0xff, 0xe0]);
    const svg = new TextEncoder().encode("<svg onload=alert(1)>");
    expect(matchesFormat(png, ImageFormat.PNG)).toBe(true);
    expect(matchesFormat(jpeg, ImageFormat.JPEG)).toBe(true);
    expect(matchesFormat(jpeg, ImageFormat.PNG)).toBe(false);
    expect(matchesFormat(svg, ImageFormat.PNG)).toBe(false);
    expect(matchesFormat(svg, ImageFormat.UNSPECIFIED)).toBe(false);
  });
});

describe("the session's pictures", () => {
  it("lists every picture in order with where it came from", () => {
    const ref = (id: string, source = ImageSource.USER) => create(ImageRefSchema, { imageId: id, source, width: 300, height: 200 });
    const list = sessionImages([
      create(EventSchema, { seq: 1n, kind: { case: "userMessage", value: { text: "[Image #1] x", images: [ref("a")] } } }),
      create(EventSchema, { seq: 2n, kind: { case: "toolCall", value: { title: "Read a.png", images: [ref("b", ImageSource.TOOL), ref("c", ImageSource.TOOL)] } } }),
      create(EventSchema, { seq: 3n, kind: { case: "imageGenerated", value: { revisedPrompt: "a hill", images: [ref("d", ImageSource.GENERATED)] } } }),
    ]);
    expect(list.map((i) => `${i.ref.imageId}:${i.kind}`)).toEqual(["a:user", "b:tool", "c:tool", "d:generated"]);
    expect(list[3].prompt).toBe("a hill");
    expect(aspectOf(list[0].ref)).toBe(1.5);
  });
});
