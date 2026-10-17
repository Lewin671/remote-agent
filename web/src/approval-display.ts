import type { ApprovalRequested } from "./gen/remoteagent/v1/event_pb";

// Approval text must reveal display controls without changing the request.
// Keep normal language, emoji, line breaks and tabs intact.
export function approvalDisplay(text: string): string {
  return text.replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/g,
    (char) => "\\u" + char.charCodeAt(0).toString(16).padStart(4, "0"));
}

// The relay already appends this text to every field it shortens.
const truncatedNote = "\n…（remote-agent：内容过长，已截断）";
export const approvalTruncatedMessage = "内容被截断，请在终端查看";
export function approvalTruncated(a: ApprovalRequested): boolean {
  return [a.description, a.command, a.toolInputJson].some(s => s.endsWith(truncatedNote));
}
