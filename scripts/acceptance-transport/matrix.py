import os,sys,time,json,uuid,base64,hashlib
from pathlib import Path
import drive as d
cli=sys.argv[1];stage=sys.argv[2]
sid=(d.ROOT/(cli+'-session')).read_text().strip()
model='claude-haiku-5-5' if cli=='claude' else 'gpt-6.1-sol'
def rpc(label,kind,v={},**kw):return d.rpc(cli,sid,label,kind,v,**kw)
def checks(label,**data):
    with (d.ROOT/'checks.jsonl').open('a') as f:f.write(json.dumps(dict(cli=cli,label=label,**data))+'\n')
    print('CHECK',cli,label,data,flush=True)
def mode(value,label=None,cut=False):
    out=rpc(label or 'mode-'+value,'setApprovalMode',{'mode':'APPROVAL_MODE_'+value},cut=cut)
    return out
def send(text,label,cut=False,send_mode=None):return d.send(cli,sid,text,label,cut,send_mode)
def finish(rid,approve=False):
    seen=set()
    def done():
        d.onboard(sid)
        rows=d.turn_events(sid,rid)
        settled={e['approvalResolved']['approvalId'] for e in rows if e.get('approvalResolved')}
        for e in rows:
            a=e.get('approvalRequested')
            if a and a['approvalId'] not in seen and a['approvalId'] not in settled:
                if not approve:raise RuntimeError('unexpected approval '+a.get('toolName',''))
                rpc('aux-approve','resolveApproval',{'approvalId':a['approvalId'],'allow':True});seen.add(a['approvalId'])
        if any(e.get('turnEnded') for e in rows):return rows
    rows=d.wait('native turn completed',done)
    d.ready(sid);return rows
if stage=='models':
    d.ready(sid)
    rpc('model-missing','setModel',{'model':'missing-transport-model','effort':'medium'},failure=True)
    rpc('effort-unsupported','setModel',{'model':model,'effort':'not-an-effort'},failure=True)
    other='claude-sonnet-5-5' if cli=='claude' else 'gpt-6-luna'
    rpc('model-switch','setModel',{'model':other,'effort':'medium'})
    rpc('model-return','setModel',{'model':model,'effort':'medium'})
    checks('native model footer',footer=d.screen(sid)['screen'].get('Footer'))
    for m in ['PLAN','ASK','FULL_ACCESS','ASK','AUTO','ASK']:
        mode(m)
        checks('native mode '+m,mode=d.screen(sid)['screen'].get('Mode'))
    rpc('rename','renameSession',{'title':'Transport regression '+cli})
    out=d.rpc(cli,'','machine-status','getMachineStatus',{})
    assert out['body'].get('machineStatus')
    checks('models and modes complete',passed=True)
elif stage=='busy':
    mode('FULL_ACCESS','busy-mode-full')
    marker=d.ROOT/'candidate/outside'/(cli+'-busy.marker');marker.write_text('running')
    pidfile=Path(str(marker.resolve())+'.pid');pidfile.unlink(missing_ok=True)
    command="python3 -c \"import os,time; from pathlib import Path; p='"+str(marker.resolve())+"'; Path(p+'.pid').write_text(str(os.getpid())); stop=time.time()+50;\nwhile os.path.exists(p) and time.time()<stop: time.sleep(.1)\""
    rid=send('Use the shell command tool to run this exact foreground command, wait for it to finish (do not background it), then reply BUSY_DONE. '+command,'busy-send')
    def started():
        rows=d.turn_events(sid,rid)
        return any(e.get('toolStarted') for e in rows) and not any(e.get('turnEnded') for e in rows) and pidfile.exists()
    d.wait('native foreground command running',started)
    held=send('Reply WITHDRAWN_ONLY after this turn.', 'midturn-send',send_mode='SEND_MODE_STEER' if cli=='claude' else 'SEND_MODE_QUEUE')
    want='INPUT_STATUS_HELD_BY_CLI' if cli=='claude' else 'INPUT_STATUS_QUEUED'
    d.wait('pending message stage '+want,lambda:next((e for e in d.events(sid) if e.get('inputState',{}).get('requestId')==held and e['inputState'].get('status')==want),None))
    rpc('cancel-pending','cancelInput',{'requestId':held})
    rpc('interrupt','interrupt')
    try:
        d.wait('native aborted turn',lambda:any(e.get('turnEnded',{}).get('outcome')=='TURN_OUTCOME_ABORTED' for e in d.turn_events(sid,rid)),timeout=30)
    finally:
        marker.unlink(missing_ok=True)
    rows=finish(rid)
    assert not any(e.get('userMessage',{}).get('requestId')==held for e in d.events(sid))
    checks('withdrawn pending message',native_held=cli=='claude',cancelled_before_model=True,turn_outcomes=[e['turnEnded']['outcome'] for e in rows if e.get('turnEnded')])
    mode('ASK','busy-return-ask')
elif stage=='approvals':
    mode('ASK','approval-mode-ask')
    for allow,cut in [(True,False),(False,False),(True,True)]:
        target=d.ROOT/'candidate/outside'/(cli+('-approved-cut-' if cut else '-approved-' if allow else '-denied-')+uuid.uuid4().hex[:8]+'.txt')
        target.unlink(missing_ok=True)
        command="python3 -c 'from pathlib import Path; p=Path(\""+str(target.resolve())+"\"); p.write_text((p.read_text() if p.exists() else \"\")+\"ONCE\\n\")'"
        prompt='Run exactly this command once with the command execution tool, not a file editing tool: '+command+'. If permission is denied, do not retry or use another tool. Just reply BLOCKED. Otherwise reply DONE.'
        if cli=='codex':prompt+=' Use elevated execution outside the sandbox and request an explicit command approval, not an ordinary question. Do not execute it without that approval.'
        rid=send(prompt,'approval-request-'+str(allow)+('-cut' if cut else ''))
        def get():
            d.onboard(sid)
            return next((e['approvalRequested'] for e in d.turn_events(sid,rid) if e.get('approvalRequested') and str(target.resolve()) in e['approvalRequested'].get('command','')),None)
        a=d.wait('native command approval',get)
        rpc('approval-cut' if cut else 'approve' if allow else 'deny','resolveApproval',{'approvalId':a['approvalId'],'allow':allow,'message':'' if allow else 'Test declined; do not retry'},cut=cut)
        rows=finish(rid)
        assert sum(1 for e in rows if e.get('approvalResolved',{}).get('approvalId')==a['approvalId'])==1
        if allow:assert target.read_text()=='ONCE\n'
        else:assert not target.exists()
        checks('approval effect',allow=allow,cut=cut,applied_once=allow,denied_without_effect=not allow)
elif stage=='questions':
    mode('PLAN','question-plan')
    text=('Use AskUserQuestion now to ask exactly one single-select question with header Language and options Go and Python. Do not do any other work.' if cli=='claude' else 'Before planning anything use request_user_input now to ask exactly one single-select question: which language, Go or Python. Do not implement or write files.')
    rid=send(text,'question-request')
    q=d.wait('native question',lambda:next((e['questionAsked'] for e in d.turn_events(sid,rid) if e.get('questionAsked')),None))
    rpc('answer-question','answerQuestion',{'questionId':q['questionId'],'options':[2]})
    rows=finish(rid)
    assert any(e.get('questionAnswered') for e in rows)
    checks('question recorded',passed=True)
    mode('ASK','question-return-ask')
elif stage=='cuts':
    mode('ASK','cut-mode-ask')
    rid=send('Reply with exactly TRANSPORT_CONTINUES, once, and nothing else.','send-cut',cut=True)
    rows=finish(rid)
    assert sum(1 for e in d.events(sid) if e.get('userMessage',{}).get('requestId')==rid)==1
    assert any('TRANSPORT_CONTINUES' in e.get('agentMessage',{}).get('text','') for e in rows)
    other='claude-sonnet-5-5' if cli=='claude' else 'gpt-6-luna'
    before=d.events(sid);seq=max(int(e.get('seq',0)) for e in before)
    rpc('model-cut','setModel',{'model':other,'effort':'medium'},cut=True)
    ev=d.events(sid,seq)
    assert sum(1 for e in ev if e.get('settingsChanged',{}).get('model')==other)==1
    rpc('model-cut-return','setModel',{'model':model,'effort':'medium'})
    checks('cut send and model',input_once=True,settings_once=True)
elif stage=='plans':
    def snapshot():
        out=d.api('/rpc',CLI=cli,Session=sid,Command={'getLiveSnapshot':{'sessionId':sid}})
        if out.get('code')!='ok':return None
        return out.get('body',{}).get('liveSnapshot')
    for choice,word in [('KEEP_PLANNING','decline'),('IMPLEMENT','accept')]:
        mode('PLAN','plan-mode-'+word)
        name=cli+'-plan-'+word+'.txt'
        target=d.ROOT/'candidate/project'/name;target.unlink(missing_ok=True)
        instruction='Create a very short implementation plan to create '+name+' containing exactly HELLO. Do not implement yet; show the native plan approval prompt. '
        if cli=='claude':instruction+='Use ExitPlanMode to present the plan for my approval.'
        rid=send(instruction,'plan-request-'+word)
        try:
            d.wait('native actionable plan',lambda:(lambda x:x if x and int(x.get('planEventSeq',0))>0 and x.get('planScreenHash') else None)(snapshot()),timeout=120)
        except RuntimeError:
            checks('plan '+word,triggered=False);continue
        rpc('plan-'+word,'answerPlan',{'choice':'PLAN_CHOICE_'+choice})
        seen=set()
        def idle():
            d.onboard(sid)
            snap=snapshot()
            if snap:
                for ev in snap.get('actions',[]):
                    a=ev.get('approvalRequested')
                    if a and a['approvalId'] not in seen:
                        if choice!='IMPLEMENT':raise RuntimeError('declined plan attempted tool work')
                        rpc('plan-implementation-approve','resolveApproval',{'approvalId':a['approvalId'],'allow':True});seen.add(a['approvalId'])
            scr=d.screen(sid)['screen']
            return scr.get('ComposerReady') and not scr.get('Busy') and not scr.get('Popup') and (choice!='IMPLEMENT' or target.exists())
        d.wait('plan action settled',idle,timeout=120)
        if choice=='IMPLEMENT':assert target.read_text().strip()=='HELLO'
        else:assert not target.exists()
        checks('plan '+word,triggered=True,passed=True,file_written=target.exists())
    mode('ASK','plan-return-ask')
elif stage in ('files-lifecycle','files-tail'):
    if stage=='files-lifecycle':
        d.ready(sid)
        seq=max(int(e.get('seq',0)) for e in d.events(sid))
        rpc('compact','compact')
        d.wait('native compaction committed',lambda:any(e.get('settingsChanged',{}).get('compacted') for e in d.events(sid,seq)))
        d.ready(sid)
        checks('native compaction committed',passed=True)
        rpc('stop','stopSession');rpc('resume','resumeSession');d.ready(sid)
    d.ready(sid)
    fork=rpc('fork','forkSession');forksid=fork['session'];d.ready(forksid)
    d.rpc(cli,forksid,'stop-fork','stopSession',{})
    name=cli+'-transport.bin';data=os.urandom((768+384)*1024);(d.ROOT/'candidate/project'/name).write_bytes(data)
    rid=send('请把当前目录的 '+name+' 发到我的手机，我要在 Remote Agent 客户端里下载原文件。','send-file-request')
    rows=finish(rid,True)
    shared=[f for e in rows for f in e.get('toolCall',{}).get('sharedFiles',[])]
    assert len(shared)==1,shared
    f=shared[0];got=b''
    for offset in [0,768*1024]:
        out=rpc('file-chunk-'+str(offset),'getSharedFileChunk',{'fileId':f['fileId'],'offset':str(offset)})
        c=out['body']['getSharedFileChunk'];assert int(c['totalBytes'])==len(data)
        block=base64.b64decode(c['data']);assert len(block)==768*1024;got+=block
    assert got[:len(data)]==data and base64.b64decode(f['sha256'])==hashlib.sha256(data).digest()
    checks('file download',bytes=len(data),chunks=2,sha256_matches=True)
    rpc('final-stop','stopSession')
else:raise RuntimeError('unknown stage')
