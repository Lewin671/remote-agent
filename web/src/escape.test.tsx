// @vitest-environment happy-dom
// Esc closes the layer on top, not every layer that is open.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { useEscape } from "./ui";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function Layer({ name, closed }: { name: string; closed: string[] }) {
  useEscape(() => closed.push(name));
  return null;
}

it("gives Esc to the layer opened last, then to the one under it", async () => {
  const closed: string[] = [];
  const host = document.createElement("div");
  const root = createRoot(host);
  const draw = (inner: boolean) => act(async () => root.render(<><Layer name="outer" closed={closed} />{inner && <Layer name="inner" closed={closed} />}</>));
  const esc = () => act(async () => { dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); });
  await draw(true);
  await esc();
  expect(closed).toEqual(["inner"]);
  await draw(false);
  await esc();
  expect(closed).toEqual(["inner", "outer"]);
  await act(async () => root.unmount());
  await esc();
  expect(closed).toEqual(["inner", "outer"]);
});
