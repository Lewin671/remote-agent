export function approvalNeedsScroll(el: HTMLElement): boolean {
  return el.scrollHeight > el.clientHeight + 4;
}

export function isApprovalAtBottom(el: HTMLElement): boolean {
  return !approvalNeedsScroll(el) || el.scrollTop + el.clientHeight >= el.scrollHeight - 8;
}

export function isShortApprovalViewport(): boolean {
  if (typeof window === "undefined") return false;
  const h = window.visualViewport?.height ?? window.innerHeight;
  return h > 0 && h < 420;
}
