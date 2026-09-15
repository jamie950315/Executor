"""Pinned user-level Beta Dashboard/relay upgrade; no credential mutation."""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import plistlib
import subprocess
import time

BASE=Path('/Users/jamie/Library/Application Support/Executor Beta')
CONFIG=BASE/'state/config.json'
LABEL='com.executor.beta.dashboard'
TARGET='gui/501/'+LABEL
PLIST=Path('/Users/jamie/Library/LaunchAgents')/(LABEL+'.plist')
STOP=BASE/'stop.py'
OLD=BASE/'bundle-ec5f53d/executor'
NEW=BASE/'bundle-hub-52a6b91/executor'
NEW_HASH='6e489fad761e3cfe3d49a4f3b1c42c49ab3e842a2edd5efe9d1cb4f00fd8e64f'
BACKUP=BASE/'hub-relay-backup-52a6b91'
OLD_EXPRESSION="('bundle-tunnel-csp' if role == 'agent' else 'bundle-ec5f53d')"

def plan(plist,helper):
    if plist.get('Label')!=LABEL or plist.get('ProgramArguments')!=[str(OLD),'dashboard','--config',str(CONFIG)] or 'UserName' in plist or plist.get('Program',str(OLD))!=str(OLD):
        raise ValueError('Unexpected Beta Dashboard service')
    if helper.count(OLD_EXPRESSION)!=1:
        raise ValueError('Unexpected Beta stop helper')
    updated=copy.deepcopy(plist)
    updated['ProgramArguments'][0]=str(NEW)
    if 'Program' in updated: updated['Program']=str(NEW)
    return updated,helper.replace(OLD_EXPRESSION,"('bundle-hub-52a6b91' if role == 'dashboard' else "+OLD_EXPRESSION+")")

def launch(*args):
    result=subprocess.run(['/bin/launchctl',*args],capture_output=True,timeout=15)
    if result.returncode: raise RuntimeError('Beta Dashboard launchctl failed: '+args[0])

def ready():
    environment={**os.environ,'EXECUTOR_STATE_DIR':str(BASE/'state')}
    deadline=time.monotonic()+25
    while time.monotonic()<deadline:
        result=subprocess.run([str(NEW),'dashboard','status','--json'],env=environment,capture_output=True,text=True,timeout=5)
        try:
            value=json.loads(result.stdout)
            if result.returncode==0 and value.get('url')=='https://beta-executor-dashboard.0ruka.dev' and value.get('relay')=='connected': return
        except ValueError: pass
        time.sleep(.2)
    raise RuntimeError('Beta relay readiness was not confirmed')

def run(apply=False):
    import beta_stop
    from beta_tunnel_upgrade import atomic
    if os.getuid()!=501: raise RuntimeError('Run as the active Beta owner')
    beta_stop.validate()
    beta_stop.probe(TARGET,PLIST)
    beta_stop.checked_stat(NEW,501)
    if hashlib.sha256(NEW.read_bytes()).hexdigest()!=NEW_HASH: raise RuntimeError('Candidate hash mismatch')
    original={p:(p.read_bytes(),p.stat()) for p in (PLIST,STOP)}
    plist,helper=plan(plistlib.loads(original[PLIST][0]),original[STOP][0].decode())
    changed={PLIST:plistlib.dumps(plist),STOP:helper.encode()}
    if not apply:
        print(json.dumps({'preflight':'passed','target':TARGET,'credentials':'preserved'}))
        return
    BACKUP.mkdir(mode=0o700,exist_ok=False)
    for index,p in enumerate((PLIST,STOP)):
        with (BACKUP/str(index)).open('xb') as out:
            os.fchmod(out.fileno(),0o600);out.write(original[p][0])
    stopped=False
    try:
        if any(p.read_bytes()!=original[p][0] for p in original): raise RuntimeError('Beta files changed before switch')
        launch('bootout',TARGET);stopped=True
        for p,data in changed.items(): atomic(p,data,original[p][1])
        launch('bootstrap','gui/501',str(PLIST))
        ready()
        print(json.dumps({'result':'installed','target':TARGET,'sha256':NEW_HASH,'backup':str(BACKUP)}))
    except Exception:
        if stopped:
            for p in original:
                if p.read_bytes() not in (original[p][0],changed[p]): raise RuntimeError('Intervening file change prevents automatic rollback') from None
            state=subprocess.run(['/bin/launchctl','print',TARGET],capture_output=True,timeout=10)
            if state.returncode==0: launch('bootout',TARGET)
            for p,(data,info) in original.items(): atomic(p,data,info)
            launch('bootstrap','gui/501',str(PLIST))
            ready()
        raise

def rollback():
    import beta_stop
    from beta_tunnel_upgrade import atomic
    if os.getuid()!=501: raise RuntimeError('Run as the active Beta owner')
    beta_stop.validate();beta_stop.probe(TARGET,PLIST)
    beta_stop.checked_stat(BACKUP,501,directory=True)
    old={p:beta_stop.checked_read(BACKUP/str(i),501)[0] for i,p in enumerate((PLIST,STOP))}
    plist,helper=plan(plistlib.loads(old[PLIST]),old[STOP].decode())
    expected={PLIST:plistlib.dumps(plist),STOP:helper.encode()}
    info={}
    for p in old:
        current,_=beta_stop.checked_read(p,501)
        if current!=expected[p]: raise RuntimeError('Independent file change prevents rollback')
        info[p]=p.stat()
    launch('bootout',TARGET)
    for p in old: atomic(p,old[p],info[p])
    launch('bootstrap','gui/501',str(PLIST));ready()
    print(json.dumps({'result':'rolled-back','target':TARGET}))

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    group=parser.add_mutually_exclusive_group();group.add_argument('--apply',action='store_true');group.add_argument('--rollback',action='store_true')
    args=parser.parse_args()
    if args.rollback: rollback()
    else: run(args.apply)
