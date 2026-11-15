// Experimental validator. Returns metadata; never rebuilds the SDP string.
export function validateSDP(bytes) {
  if (bytes.length > 60 * 1024 || (bytes[0]===239 && bytes[1]===187 && bytes[2]===191)) throw Error('size');
  const raw = new TextDecoder('utf-8', {fatal:true}).decode(bytes);
  if (!raw.endsWith('\r\n') || raw.includes('\0') || /[\v\f\u0085\u2028\u2029]/.test(raw) || /(^|[^\r])\n|\r(?!\n)/.test(raw)) throw Error('lines');
  let section = 0;
  const scopes = [new Map(), new Map()];
  for (const line of raw.slice(0,-2).split('\r\n')) {
    if (/^m=/.test(line)) {
      if (++section !== 1 || !/^m=application [1-9][0-9]* UDP\/DTLS\/SCTP webrtc-datachannel$/.test(line)) throw Error('media');
    }
    if (line === 'a=ice-lite') throw Error('ice-lite');
    if (line.startsWith('a=')) {
      const match = /^a=([^:]+)(?::(.*))?$/.exec(line);
      const name = match[1], value = match[2] ?? '';
      const allowed = new Set(['group','msid-semantic','fingerprint','ice-ufrag','ice-pwd','ice-options','setup','mid','sctp-port','max-message-size','end-of-candidates','extmap-allow-mixed','sendrecv']);
      if (!allowed.has(name)) throw Error('attribute');
      if (scopes[section].has(name)) throw Error('duplicate');
      scopes[section].set(name, value);
    } else if (!/^[vostcm]=/.test(line)) throw Error('line');
  }
  if (section !== 1) throw Error('media');
  const fps = scopes.map(s=>s.get('fingerprint')).filter(v=>v!==undefined);
  if (!fps.length || fps.some(v=>!/^sha-256 (?:[0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}$/.test(v))) throw Error('fingerprint');
  if (new Set(fps.map(v=>v.toUpperCase())).size!==1) throw Error('fingerprint-conflict');
  const values={};
  for(const attr of ['ice-ufrag','ice-pwd','setup']) {
    const all=scopes.map(s=>s.get(attr)).filter(v=>v!==undefined);
    if(!all.length || new Set(all).size!==1) throw Error('ice-inheritance');
    values[attr]=all[0];
  }
  if(!/^[A-Za-z0-9+/]{4,256}$/.test(values['ice-ufrag']) || !/^[A-Za-z0-9+/]{22,256}$/.test(values['ice-pwd'])) throw Error('credentials');
  if(!['active','passive','actpass'].includes(values.setup)) throw Error('setup');
  if(!/^[0-9]+$/.test(scopes[1].get('sctp-port')??'') || Number(scopes[1].get('sctp-port'))>65535) throw Error('sctp');
  return {raw, ufrag:values['ice-ufrag'], fingerprint:fps[0], mid:scopes[1].get('mid')};
}
