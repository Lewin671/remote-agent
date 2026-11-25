import { afterEach, expect, test, vi } from "vitest";
import { Link } from "./link";
import { RelayPath, RelayStream } from "./relay-path";

afterEach(() => vi.unstubAllGlobals());

test("physical path endings are separate from Link's relay-only stream-close policy", () => {
  let socket!: { onclose?: () => void };
  vi.stubGlobal("WebSocket", class {
    constructor() { socket = this; }
    onclose?: () => void;
    close() { this.onclose?.(); }
  });
  const path = new RelayStream("wss://example.com/v1/term");
  const pathEnd = vi.fn(), productClose = vi.fn(), stop = vi.fn();
  path.onEnd = pathEnd;
  const link = new Link(new RelayPath(async () => "epoch", vi.fn()));
  const stream = link.openStream(productClose, stop);
  socket.onclose?.();
  expect(pathEnd).toHaveBeenCalledTimes(1);
  expect(productClose).not.toHaveBeenCalled();
  link.endRelayStreams("closed");
  stream.end("late path callback");
  expect(productClose).toHaveBeenCalledExactlyOnceWith("closed");
  expect(stop).toHaveBeenCalledTimes(1);
});
