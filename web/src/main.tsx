import * as l10n from "./i18n/startup";
// Keep the HTML shell paintable while the app and its stylesheet download.
import { applyFonts, applyTheme } from "./theme";

applyTheme();
applyFonts();

void import("./app-entry").catch(() => {
  const status = document.getElementById("startup-status");
  if (status) status.textContent = l10n.t("startup.the_page_could_not_load_check_your_connection_and");
  document.getElementById("startup-retry")?.removeAttribute("hidden");
});
