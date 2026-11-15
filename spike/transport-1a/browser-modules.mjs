// Public browser-only modules. Both runners use this list; the graph check in
// verify.py fails when a transitive import is missing or a module is unused.
export const browserModules=new Set(['sender-check.mjs','signal-replay.mjs','sdp.mjs','sender.mjs','signal.mjs','wire.mjs','schema.mjs','schema-data.mjs','schema-bounds.mjs','schema-relations.mjs']);
