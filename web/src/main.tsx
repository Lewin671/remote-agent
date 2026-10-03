import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles.css";
import { registerWorker } from "./push";
import { applyFonts, applyTheme } from "./theme";

// Before the first paint, so a dark page does not flash light.
applyTheme();
applyFonts();
registerWorker();

// A chunk that fails to load belongs to a build the server has replaced.
// The kept copy of the page is dropped, so that the service worker does
// not answer with that build again; and if the chunk was needed now, the
// page starts over on the current build. One that was only being fetched
// ahead interrupts nothing: the next visit, or the first need, moves on.
let ahead = false;
addEventListener("vite:preloadError", (e) => {
  e.preventDefault();
  const needed = !ahead;
  const last = Number(sessionStorage.getItem("ra-reloaded"));
  void (async () => {
    try {
      await (await caches.open("shell-v1")).delete("/", { ignoreVary: true });
    } catch {
      // No cache storage: the network answers anyway.
    }
    if (!needed || Date.now() - last < 60_000) return;
    sessionStorage.setItem("ra-reloaded", String(Date.now()));
    location.reload();
  })();
});

// The parts fetched on demand are fetched ahead once the page has settled,
// so that they are at hand later: offline, or after the server moved on
// to a newer build.
setTimeout(() => {
  ahead = true;
  const parts = [() => import("./views/Terminal"), () => import("./views/Pairing"), () => import("./sendimages"), () => import("@noble/curves/ed25519.js")];
  void Promise.allSettled(parts.map((part) => part())).then(() => (ahead = false));
}, 4000);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
