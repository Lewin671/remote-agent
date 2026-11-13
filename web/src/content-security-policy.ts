export const contentSecurityPolicy = (themeHash: string): string[] => [
  "default-src 'self'",
  `script-src 'self' 'sha256-${themeHash}'`,
  "style-src 'self' 'unsafe-inline'",
  // Session images are shown from decrypted bytes as blob: URLs.
  "img-src 'self' blob: data:",
  "media-src 'self' blob:",
  "connect-src 'self' https: wss: http://localhost:* ws://localhost:* http://127.0.0.1:* ws://127.0.0.1:*",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
];
