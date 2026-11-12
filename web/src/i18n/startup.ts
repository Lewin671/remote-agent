import { startupMessages } from "./startup.generated";

/** Launch text has no React or main-app dependency. Stage 1 stays Chinese. */
export function t(key: keyof typeof startupMessages["zh-CN"], locale: keyof typeof startupMessages = "zh-CN"): string {
  return startupMessages[locale][key];
}
