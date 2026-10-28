// Keep the QR library out of the initial client bundle. Components depend on
// this async operation, so loading/error/cancellation can be tested directly.
export async function pairingQRCode(url: string): Promise<string> {
  const { default: qrcode } = await import("qrcode-generator");
  const q = qrcode(0, "M");
  q.addData(url);
  q.make();
  return q.createDataURL(5, 8);
}
