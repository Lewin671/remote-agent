import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles.css";
import { registerWorker } from "./push";
import { applyFonts, applyTheme } from "./theme";
import { watchUpdates } from "./update";
import { fetchAhead, watchChunks } from "./chunks";
import { pagesAhead } from "./views/pages";

// Before the first paint, so a dark page does not flash light.
applyTheme();
applyFonts();
registerWorker();
watchUpdates();

watchChunks();

// The parts fetched on demand are fetched ahead once the page has settled:
// the terminal, pairing, the pages of settings and tasks.
fetchAhead([...pagesAhead, () => import("./views/Pairing"), () => import("./views/Terminal"), () => import("./sendimages"), () => import("@noble/curves/ed25519.js")]);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
