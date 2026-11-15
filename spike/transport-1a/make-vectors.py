#!/usr/bin/env python3
"""Copy raw, ephemeral SDK SDP fixtures and derive named negative vectors."""
import argparse
import hashlib
import json
from pathlib import Path
import re

p=argparse.ArgumentParser();p.add_argument('source',type=Path);p.add_argument('output',type=Path);args=p.parse_args()
args.output.mkdir(parents=True,exist_ok=True)
rows=[];metadata=[]
def add(name,data,valid,origin):
    (args.output/name).write_bytes(data);rows.append(name+'\t'+str(int(valid)))
    metadata.append(dict(file=name,accept=valid,source=origin,sha256=hashlib.sha256(data).hexdigest(),bytes=len(data)))
for name in ['pion','chrome','webkit','android','ios']:
    for kind in ['offer','answer','restart']:
        path=args.source/(name+'-'+kind+'.sdp')
        add(path.name,path.read_bytes(),True,'SDK original; ephemeral test peer')
raw=(args.source/'chrome-offer.sdp').read_bytes()
fp=re.search(rb'a=fingerprint:[^\r]+\r\n',raw).group()
ufrag=re.search(rb'a=ice-ufrag:[^\r]+\r\n',raw).group()
vectors={
    'lf-only':raw.replace(b'\r\n',b'\n'),
    'bare-cr':raw.replace(b'\r\n',b'\r',1),
    'no-final-crlf':raw[:-2],
    'nul':raw+b'\0',
    'invalid-utf8':raw.replace(b's=-',b's=\xc0\xaf'),
    'audio':raw+b'm=audio 9 UDP/TLS/RTP/SAVPF 111\r\n',
    'two-application':raw+raw[raw.index(b'm=application'):],
    'ice-lite':raw.replace(b'm=application',b'a=ice-lite\r\nm=application',1),
    'duplicate-fingerprint':raw+fp,
    'fingerprint-sha1':raw.replace(b'fingerprint:sha-256',b'fingerprint:sha-1'),
    'fingerprint-short':raw.replace(fp,b'a=fingerprint:sha-256 01:02\r\n'),
    'fingerprint-missing':raw.replace(fp,b''),
    'fingerprint-conflict':raw.replace(b'm=application',b'a=fingerprint:sha-256 '+b'00:'*31+b'00\r\nm=application',1),
    'bom':b'\xef\xbb\xbf'+raw,
    'duplicate-ufrag':raw+ufrag,
    'unknown-media':raw.replace(b'm=application',b'm=unknown'),
    'oversize':raw+b'a=candidate:'+b'A'*65536+b'\r\n',
}
for name,data in vectors.items():add('bad-'+name+'.bin',data,False,'mutation of chrome-offer.sdp')
inherited=raw.replace(b'm=application',fp+b'm=application',1)
add('good-inherited-fingerprint.sdp',inherited,True,'same fingerprint at session and media scopes')
assert len(rows)==33
(args.output/'vectors.tsv').write_text('\n'.join(rows)+'\n')
(args.output/'manifest.json').write_text(json.dumps(metadata,indent=2)+'\n')
(args.output/'.gitattributes').write_text('*.sdp -text -diff\n*.bin -text -diff\n')
print('vectors:',len(rows))
