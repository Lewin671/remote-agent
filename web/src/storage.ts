import { watchAccountLogout } from "./core/security-store-v4";

export interface StoredAccount {
  server: string;
  deviceId: string;
  accountId?: string;
}

export { watchAccountLogout };
