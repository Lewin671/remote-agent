"""Drive only the guarded loopback controller; does not invoke remote-agent."""
import base64, hashlib, json, os, time, uuid, urllib.request
from pathlib import Path
ROOT = Path(os.environ['RA_DEV_DIR'])
assert str(ROOT).startswith(('/tmp/','/private/tmp/'))
assert os.environ['REMOTE_AGENT_HOME'] == str(ROOT/'bootstrap')
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
def api(path, **data):
    url = (ROOT/'control-url').read_text().strip()
    req = urllib.request.Request(url+path, data=json.dumps(data).encode(), headers={'Content-Type':'application/json','X-Transport-Test':(ROOT/'control-token').read_text().strip()})
    with OPENER.open(req, timeout=160) as f:
        return json.load(f)
def rpc(cli, sid, label, kind, value, *, cut=False, failure=False):
    value = dict(value)
    if sid and kind not in ('createSession','listModels','getMachineStatus','getSettings'):
        value.setdefault('sessionId',sid)
    out=api('/rpc', Label=label,CLI=cli,Session=sid,Command={kind:value},Cut=cut)
    print(cli,label,'code='+out.get('code','missing'),'unknown='+str(out.get('unknown')),'signed='+str(out.get('signed')),'cut='+str(out.get('cutVerified',False)),flush=True)
    ok=out.get('code')=='ok' and not out.get('unknown')
    if failure: ok=out.get('signed') and out.get('code')!='ok' and not out.get('unknown')
    if not ok: raise RuntimeError(label+': '+json.dumps({k:v for k,v in out.items() if k!='body'})[:800])
    return out
def ready(sid):
    out=api('/ready',Session=sid)
    if out.get('code')!='ok':raise RuntimeError('not ready: '+str(out))
    def owner_idle():
        x=api('/rpc',Session=sid,Command={'getLiveSnapshot':{'sessionId':sid}})
        b=x.get('body',{}).get('liveSnapshot',{})
        return x.get('code')=='ok' and b.get('session',{}).get('status')=='SESSION_STATUS_IDLE' and not b.get('running')
    wait('signed owner idle',owner_idle,timeout=30)
def events(sid, after=0):
    out=api('/events',Session=sid,After=after)
    if not isinstance(out,list):raise RuntimeError(str(out))
    return out
def screen(sid):return api('/screen',Session=sid)
def wait(what,fn,timeout=120):
    stop=time.monotonic()+timeout
    last=0
    while time.monotonic()<stop:
        v=fn()
        if v:return v
        if time.monotonic()-last>25:print('WAIT',what,flush=True);last=time.monotonic()
        time.sleep(.25)
    raise RuntimeError('timeout: '+what)
def create(cli):
    model='claude-haiku-5-5' if cli=='claude' else 'gpt-6.1-sol'
    args=['--allow-dangerously-skip-permissions'] if cli=='claude' else ['-c','approvals_reviewer="user"']
    out=rpc(cli,'','create','createSession',{'agent':'AGENT_CLAUDE' if cli=='claude' else 'AGENT_CODEX','cwd':str((ROOT/'candidate/project').resolve()),'model':model,'mode':'APPROVAL_MODE_ASK','args':args})
    sid=out['session'];ready(sid)
    env=json.loads((ROOT/'candidate/machine/sessions'/sid/'env.json').read_text())
    for k,rel in [('REMOTE_AGENT_HOME','candidate/machine'),('CODEX_HOME','candidate/native-codex'),('CLAUDE_CONFIG_DIR','candidate/native-claude')]:
        assert Path(env[k]).resolve()==(ROOT/rel).resolve(),k
    print(cli,'native persisted isolation verified',flush=True)
    (ROOT/(cli+'-session')).write_text(sid)
    return sid

def send(cli,sid,text,label='send',cut=False,mode=None):
    rid=uuid.uuid4().hex
    body={'text':text,'requestId':rid}
    if mode:body['mode']=mode
    rpc(cli,sid,label,'send',body,cut=cut)
    return rid

def turn_events(sid,rid):
    rows=events(sid)
    start=next((e for e in rows if e.get('userMessage',{}).get('requestId')==rid),None)
    if not start:return []
    # Some native response_item tool events have no turnId. Anchor on the
    # authenticated user message's exact request ID, then its sequence window.
    out=[]
    for e in rows:
        if int(e.get('seq',0))<int(start.get('seq',0)):continue
        if e is not start and e.get('userMessage'):break
        out.append(e)
        if e.get('turnEnded') and e.get('turnId')==start.get('turnId'):break
    return out
def finish(sid,rid,approve=False):
    seen=set()
    def done():
        rows=turn_events(sid,rid)
        settled={e['approvalResolved']['approvalId'] for e in rows if e.get('approvalResolved')}
        for e in rows:
            a=e.get('approvalRequested')
            if a and a['approvalId'] not in seen and a['approvalId'] not in settled:
                if not approve:raise RuntimeError('unexpected approval: '+a.get('toolName',''))
                cli='claude' if sid==(ROOT/'claude-session').read_text() else 'codex'
                rpc(cli,sid,'aux-approve','resolveApproval',{'approvalId':a['approvalId'],'allow':True})
                seen.add(a['approvalId'])
        if any(e.get('turnEnded') for e in rows):return rows
    out=wait('turn completion',done)
    ready(sid)
    return out

if __name__=='__main__':
    import sys
    cli=sys.argv[1]
    sid=create(cli)
    rid=send(cli,sid,'Reply with exactly PONG and nothing else.','send')
    rows=finish(sid,rid)
    assert any('PONG' in e.get('agentMessage',{}).get('text','') for e in rows)
    print('PASS',cli,'encrypted create/send/native reply',flush=True)

def onboard(sid):
    # Only known initial trust/onboarding for this task's own native process.
    # Never answers tool approvals or questions here.
    import subprocess
    assert (ROOT/'candidate/machine/sessions'/sid).is_dir()
    socket=(ROOT/'socket').read_text().strip();assert socket.startswith('ra-t4-')
    pane='ra-'+sid[:8]+':'
    raw=subprocess.run(['tmux','-L',socket,'capture-pane','-p','-t',pane],capture_output=True,text=True).stdout
    keys=[]
    if 'Hooks need review' in raw:keys=['Down','Enter']
    elif 'WARNING: Claude Code running in Bypass Permissions mode' in raw:keys=['Down','Enter']
    elif 'Make auto mode your default permission mode?' in raw:
        keys=['Enter'] if '❯ No, keep manual mode' in raw else ['Down','Enter']
    for k in keys:
        subprocess.run(['tmux','-L',socket,'send-keys','-t',pane,k],check=True)
        time.sleep(.2)
    if keys:print('DEBUG isolated startup trust/onboarding',flush=True)
