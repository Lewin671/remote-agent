// Keep the HTML shell paintable while the app and its stylesheet download.
import { applyFonts, applyTheme } from "./theme";

applyTheme();
applyFonts();

void import("./app-entry").catch(() => {
  const status = document.getElementById("startup-status");
  if (status) status.textContent = "页面未能加载，请检查网络后重试。";
  document.getElementById("startup-retry")?.removeAttribute("hidden");
});
