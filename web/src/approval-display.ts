// Approval text must reveal display controls without changing the request.
// Keep normal language, emoji, line breaks and tabs intact.
export function approvalDisplay(text: string): string {
  return text.replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/g,
    (char) => "\\u" + char.charCodeAt(0).toString(16).padStart(4, "0"));
}
