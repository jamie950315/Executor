"""One-revision, Beta-Agent-only upgrade. No credential reads or rotation."""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import plistlib
import subprocess
import tempfile
import time
import urllib.request

BASE = Path('/Users/jamie/Library/Application Support/Executor Beta')
STATE = BASE / 'state'
CONFIG = STATE / 'config.json'
LABEL = 'com.executor.beta.agent'
PLIST = Path('/Library/LaunchDaemons') / (LABEL + '.plist')
OLD = BASE / 'bundle-ec5f53d/executor'
NEW = BASE / 'bundle-tunnel-1cc79b1/executor'
NEW_HASH = '5f89d56eeb72cc2d2c26f05518ca97280bedda24f1829519643facc348949c4b'
DOMAIN = 'beta-executor-mac.0ruka.dev'
ALIAS = 'https://tunnel-service.gateway.unified-0.internal.api.openai.org/v1/mcp/tunnel_6aa821f0b7c0819190016de041dfc881'
BACKUP = BASE / 'deployment-backups/tunnel-agent-1cc79b1'
STOP = BASE / 'stop.py'
STOP_OLD = "return [str(BASE / 'bundle-ec5f53d' / relative), role, '--config', str(STATE / 'config.json')]"
FILES = (CONFIG, PLIST, STOP)

def planned_config(cfg, plist):
    expected = {'domain': DOMAIN, 'agent_address': '127.0.0.1:18787', 'state_dir': str(STATE)}
    if any(cfg.get(k) != v for k, v in expected.items()):
        raise ValueError('Unexpected Beta configuration')
    argv = [str(OLD), 'agent', '--config', str(CONFIG)]
    if plist.get('Label') != LABEL or plist.get('ProgramArguments') != argv or plist.get('UserName') != 'jamie':
        raise ValueError('Unexpected Beta Agent service')
    if cfg.get('oauth_resource_aliases'):
        raise ValueError('Existing aliases require separate review')
    new_cfg, new_plist = copy.deepcopy(cfg), copy.deepcopy(plist)
    new_cfg['oauth_resource_aliases'] = [ALIAS]
    new_plist['ProgramArguments'][0] = str(NEW)
    if 'Program' in new_plist:
        if new_plist['Program'] != str(OLD):
            raise ValueError('Unexpected Program override')
        new_plist['Program'] = str(NEW)
    return new_cfg, new_plist

def updated_stop(text):
    if text.count(STOP_OLD) != 1:
        raise ValueError('Unexpected stop helper version')
    return text.replace(STOP_OLD, "return [str(BASE / ('bundle-tunnel-1cc79b1' if role == 'agent' else 'bundle-ec5f53d') / relative), role, '--config', str(STATE / 'config.json')]")

def launch(*args):
    result = subprocess.run(['/bin/launchctl', *args], capture_output=True, timeout=15)
    if result.returncode:
        raise RuntimeError('Beta launchctl operation failed: ' + args[0])

def atomic(path, data, info):
    fd, temp = tempfile.mkstemp(prefix='.tunnel-upgrade-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as out:
            os.fchmod(out.fileno(), info.st_mode & 0o777)
            os.fchown(out.fileno(), info.st_uid, info.st_gid)
            out.write(data)
            out.flush()
            os.fsync(out.fileno())
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)

def ready():
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen('http://127.0.0.1:18787/.well-known/oauth-authorization-server', timeout=2) as response:
                if json.load(response).get('issuer') == 'https://' + DOMAIN:
                    return
        except (OSError, ValueError):
            pass
        time.sleep(.2)
    raise RuntimeError('Beta OAuth readiness failed')

def run(apply=False):
    # Reuse the existing strict ancestor/owner/service preflight.
    import beta_stop
    beta_stop.validate()
    beta_stop.probe('system/' + LABEL, PLIST)
    beta_stop.checked_stat(NEW, 501)
    if hashlib.sha256(NEW.read_bytes()).hexdigest() != NEW_HASH:
        raise ValueError('Candidate hash mismatch')
    original = {p: (p.read_bytes(), p.stat()) for p in FILES}
    cfg, plist = planned_config(json.loads(original[CONFIG][0]), plistlib.loads(original[PLIST][0]))
    changes = {
        CONFIG: (json.dumps(cfg, indent=2) + '\n').encode(),
        PLIST: plistlib.dumps(plist),
        STOP: updated_stop(original[STOP][0].decode()).encode(),
    }
    if not apply:
        print(json.dumps({'preflight': 'passed', 'target': LABEL, 'credential_rotation': False}))
        return
    if os.geteuid() != 0:
        raise PermissionError('Beta service update requires administrator authority')
    BACKUP.mkdir(mode=0o700, parents=False, exist_ok=False)
    for index, p in enumerate(FILES):
        with (BACKUP / str(index)).open('xb') as out:
            os.fchmod(out.fileno(), 0o600)
            out.write(original[p][0])
    stopped, loaded = False, False
    try:
        launch('bootout', 'system/' + LABEL)
        stopped = True
        for p in FILES:
            if p.read_bytes() != original[p][0]:
                raise RuntimeError('Beta files changed after preflight')
        for p, data in changes.items():
            atomic(p, data, original[p][1])
        launch('bootstrap', 'system', str(PLIST))
        loaded = True
        ready()
        print(json.dumps({'result': 'installed', 'target': LABEL, 'backup': str(BACKUP)}))
    except Exception:
        if loaded:
            launch('bootout', 'system/' + LABEL)
        if stopped:
            for p, (data, info) in original.items():
                atomic(p, data, info)
            launch('bootstrap', 'system', str(PLIST))
            ready()
        raise

def rollback():
    if os.geteuid() != 0:
        raise PermissionError('Rollback requires administrator authority')
    import beta_stop
    beta_stop.checked_stat(BACKUP, 0, directory=True)
    saved = {p: beta_stop.checked_read(BACKUP / str(i), 0)[0] for i, p in enumerate(FILES)}
    cfg, plist = planned_config(json.loads(saved[CONFIG]), plistlib.loads(saved[PLIST]))
    expected = {CONFIG: (json.dumps(cfg, indent=2)+'\n').encode(),
                PLIST: plistlib.dumps(plist), STOP: updated_stop(saved[STOP].decode()).encode()}
    identities = {}
    for p in FILES:
        current, _ = beta_stop.checked_read(p)
        if current != expected[p]:
            raise ValueError('Rollback refuses independently changed Beta files')
        identities[p] = p.stat()
    launch('bootout', 'system/' + LABEL)
    for p in FILES:
        atomic(p, saved[p], identities[p])
    launch('bootstrap', 'system', str(PLIST))
    ready()
    print(json.dumps({'result': 'rolled-back', 'target': LABEL}))

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--rollback', action='store_true')
    args = parser.parse_args()
    if args.apply and args.rollback:
        parser.error('Choose apply or rollback, not both')
    if args.rollback:
        rollback()
    else:
        run(args.apply)
