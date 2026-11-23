import { clearAllSecurityV4, securityStoreV4, watchAccountLogout } from "./core/security-store-v4";

export interface StoredAccount {
  server: string;
  deviceId: string;
  accountId?: string;
}

export { watchAccountLogout };

/** Forgets the local security credentials, keys, token, and cached sealed history. */
export async function forgetAccount(): Promise<void> {
  await clearAllSecurityV4(securityStoreV4);
}
