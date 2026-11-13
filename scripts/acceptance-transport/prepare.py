"""Prepare/build the opt-in local transport harness. Never runs remote-agent."""
import argparse,json,os,shutil,subprocess
from pathlib import Path
parser=argparse.ArgumentParser();parser.add_argument('--copy-auth',action='store_true');args=parser.parse_args()
root=Path(os.environ['RA_DEV_DIR']);assert root.is_absolute() and str(root.resolve()).startswith(('/tmp/','/private/tmp/'))
assert os.environ['REMOTE_AGENT_HOME']==str(root/'bootstrap')
repo=Path(__file__).resolve().parents[2]
for rel in ['bootstrap','bin','candidate/machine','candidate/native-home','candidate/native-claude','candidate/native-codex','candidate/project','candidate/outside','trace']:(root/rel).mkdir(mode=0o700,parents=True,exist_ok=True)
root.chmod(0o700)
if args.copy_auth:
    # Explicit opt-in, credentials/cache only. No daily sessions, MCP settings,
    # project rules or remote-agent state is copied.
    home=Path.home();codex=root/'candidate/native-codex';claude=root/'candidate/native-claude'
    for name in ['auth.json','models_cache.json']:
        source=home/'.codex'/name
        if source.exists():shutil.copyfile(source,codex/name);(codex/name).chmod(0o600)
    source=home/'.claude/.credentials.json'
    if source.exists():raw=source.read_bytes()
    else:
        p=subprocess.run(['/usr/bin/security','find-generic-password','-s','Claude Code-credentials','-w'],capture_output=True,check=True);raw=p.stdout
    assert 'claudeAiOauth' in json.loads(raw)
    (claude/'.credentials.json').write_bytes(raw);(claude/'.credentials.json').chmod(0o600)
    (claude/'.claude.json').write_text(json.dumps({'hasCompletedOnboarding':True,'theme':'dark'}))
    project=str((root/'candidate/project').resolve())
    (codex/'config.toml').write_text('model = "gpt-6.1-sol"\nmodel_reasoning_effort = "medium"\napproval_policy = "on-request"\nsandbox_mode = "workspace-write"\napprovals_reviewer = "user"\n[projects.'+json.dumps(project)+']\ntrust_level = "trusted"\n')
    print('Prepared only isolated native authentication and minimal settings.')
source=repo/'cmd/remote-agent/commands.go';code=source.read_text();needle='relay.New(backend{d}).Run(ctx)';assert code.count(needle)==1
(root/'commands-overlay.go').write_text(code.replace(needle,'relay.New(transportAcceptanceBackend(backend{d})).Run(ctx)'))
(root/'observer-overlay.go').write_text((repo/'scripts/acceptance-transport/observer.go.txt').read_text())
(root/'overlay.json').write_text(json.dumps({'Replace':{str(source):str(root/'commands-overlay.go'),str(repo/'cmd/remote-agent/transport_acceptance_overlay.go'):str(root/'observer-overlay.go')}}))
env=dict(os.environ,CGO_ENABLED='0')
for output,package,extra in [('controller','./scripts/acceptance-transport',[]),('remote-agent-server','./cmd/remote-agent-server',[]),('remote-agent','./cmd/remote-agent',['-overlay',str(root/'overlay.json')])]:
    free=shutil.disk_usage('/System/Volumes/Data').free;print('DISK_FREE_BYTES',free,flush=True)
    if free<15*1024**3:raise SystemExit('Stopped: less than 15 GiB available')
    subprocess.run(['go','build',*extra,'-o',str(root/'bin'/output),package],cwd=repo,env=env,check=True)
print('Built current checkout server, daemon with acceptance-only observer overlay, and controller.')
