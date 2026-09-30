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

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
