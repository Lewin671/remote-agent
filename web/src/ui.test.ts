// @vitest-environment happy-dom
import { expect, it } from "vitest";
import { fromHere } from "./ui";

it("a command that ends navigates only from the page that sent it", () => {
  const went: string[] = [];
  const go = (route: string) => void went.push(route);
  location.hash = "/c/phone";
  // Still on its page when the answer comes.
  fromHere(go)("devices");
  expect(went).toEqual(["devices"]);
  // The answer is slow; the user has opened another page meanwhile, which stays.
  const leave = fromHere(go);
  location.hash = "/m/mac";
  leave("devices");
  expect(went).toEqual(["devices"]);
});
