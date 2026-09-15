"""Strict four-service stop for this exact Mac Beta. --check is read-only."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import stat
import subprocess

BASE = Path('/Users/jamie/Library/Application Support/Executor Beta')
STATE = BASE / 'state'
USER_PLISTS = Path('/Users/jamie/Library/LaunchAgents')
SYSTEM_PLISTS = Path('/Library/LaunchDaemons')
OWNER_UID, SYSTEM_UID = 501, 0
ROLES = ('dashboard', 'desktop', 'agent', 'broker')
TARGETS = (
    ('gui/501/com.executor.beta.dashboard', USER_PLISTS / 'com.executor.beta.dashboard.plist'),
    ('gui/501/com.executor.beta.desktop', USER_PLISTS / 'com.executor.beta.desktop.plist'),
    ('system/com.executor.beta.agent', SYSTEM_PLISTS / 'com.executor.beta.agent.plist'),
    ('system/com.executor.beta.broker', SYSTEM_PLISTS / 'com.executor.beta.broker.plist'),
)

class SafetyError(Exception):
    pass

def checked_stat(path, owner=None, directory=False):
    path = Path(path)
    if not path.is_absolute():
        raise SafetyError('Expected absolute path')
    for parent in reversed(path.parents):
        info = parent.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid not in {0, OWNER_UID, SYSTEM_UID} or info.st_mode & 0o022:
            raise SafetyError('Untrusted/symlink ancestor: ' + str(parent))
    info = path.lstat()
    owners = {owner} if owner is not None else {OWNER_UID, SYSTEM_UID}
    valid_type = stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode) and info.st_nlink == 1
    if not valid_type or info.st_uid not in owners or info.st_mode & 0o022:
        raise SafetyError('Untrusted file type/owner/mode: ' + str(path))
    return info

def checked_read(path, owner=None):
    before = checked_stat(path, owner)
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), 'rb') as source:
        info = os.fstat(source.fileno())
        if (before.st_dev, before.st_ino, before.st_mode, before.st_uid) != (info.st_dev, info.st_ino, info.st_mode, info.st_uid):
            raise SafetyError('File changed during validation')
        data = source.read()
    return data, (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid, hashlib.sha256(data).hexdigest())

def expected_argv(role):
    relative = 'Executor Desktop.app/Contents/MacOS/executor-desktop' if role == 'desktop' else 'executor'
    bundle = 'bundle-tunnel-csp' if role == 'agent' else 'bundle-ec5f53d'
    return [str(BASE / bundle / relative), role, '--config', str(STATE / 'config.json')]

def validate():
    checked_stat(BASE, OWNER_UID, directory=True)
    checked_stat(STATE, OWNER_UID, directory=True)
    identities = {}
    def read(path, owner):
        data, identity = checked_read(path, owner)
        identities[str(path)] = identity
        return data
    cfg = json.loads(read(STATE / 'config.json', OWNER_UID))
    expected = {'domain': 'beta-executor-mac.0ruka.dev', 'state_dir': str(STATE),
                'agent_address': '127.0.0.1:18787', 'dashboard_address': '127.0.0.1:18788',
                'broker_endpoint': str(STATE / 'broker.sock'), 'desktop_endpoint': str(STATE / 'desktop.sock')}
    if not isinstance(cfg, dict) or any(cfg.get(k) != v for k, v in expected.items()):
        raise SafetyError('Beta state identity mismatch')
    if len(TARGETS) != 4:
        raise SafetyError('Expected all four targets')
    for role, (target, path) in zip(ROLES, TARGETS):
        gui = role in ('dashboard', 'desktop')
        label = 'com.executor.beta.' + role
        if target != (f'gui/{OWNER_UID}/' if gui else 'system/') + label or path != (USER_PLISTS if gui else SYSTEM_PLISTS) / (label + '.plist'):
            raise SafetyError('Unexpected target/plist')
        doc = plistlib.loads(read(path, OWNER_UID if gui else SYSTEM_UID))
        argv = expected_argv(role)
        if not isinstance(doc, dict) or doc.get('Label') != label or doc.get('ProgramArguments') != argv or doc.get('Program', argv[0]) != argv[0]:
            raise SafetyError('Service ownership mismatch: ' + target)
        if doc.get('UserName') != (None if gui else ('root' if role == 'broker' else 'jamie')):
            raise SafetyError('Unexpected service user: ' + target)
        env = doc.get('EnvironmentVariables', {})
        if not isinstance(env, dict) or any(env.get(k, v) != v for k, v in {'EXECUTOR_CONFIG_PATH': str(STATE / 'config.json'), 'EXECUTOR_STATE_DIR': str(STATE)}.items()):
            raise SafetyError('Unexpected state override: ' + target)
        if not checked_stat(Path(argv[0]), OWNER_UID).st_mode & 0o111:
            raise SafetyError('Non-executable binary')
        read(Path(argv[0]), OWNER_UID)
    marker = STATE / 'disabled'
    if os.path.lexists(marker):
        info = checked_stat(marker)
        if stat.S_IMODE(info.st_mode) != 0o600:
            raise SafetyError('Unsafe disabled marker mode')
        read(marker, info.st_uid)
    return identities

def launchctl(*args):
    return subprocess.run(['/bin/launchctl', *args], capture_output=True, text=True, timeout=10)

def probe(target, path):
    result = launchctl('print', target)
    label = target.rsplit('/', 1)[1]
    if result.returncode == 113 and f'Could not find service "{label}" in domain' in result.stderr:
        return 'already_unloaded'
    if result.returncode != 0:
        raise SafetyError('Cannot determine service state: ' + target)
    lines = result.stdout.splitlines()
    loaded_path = next((s.strip()[7:] for s in lines if s.strip().startswith('path = ')), None)
    program = next((s.strip()[10:] for s in lines if s.strip().startswith('program = ')), None)
    match = re.search(r'^\s*arguments = \{\n(.*?)^\s*\}', result.stdout, re.M | re.S)
    argv = [s.strip() for s in match.group(1).splitlines() if s.strip()] if match else None
    if loaded_path != str(path) or program != expected_argv(label.rsplit('.', 1)[1])[0] or argv != expected_argv(label.rsplit('.', 1)[1]):
        raise SafetyError('Loaded service identity mismatch: ' + target)
    return 'loaded'

def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args(argv)
    try:
        identities = validate()
        states = [probe(target, path) for target, path in TARGETS]
        if args.check:
            for (target, _), state in zip(TARGETS, states):
                print(target + ': ' + state)
            print('Validated four Beta-only targets; no marker/service changes. Shared Tunnel excluded.')
            return 0
        if os.geteuid() != 0:
            raise SafetyError('Stopping Beta system services requires sudo')
        if identities != validate():
            raise SafetyError('Files changed before stop')
        descriptor = os.open(STATE / 'disabled', os.O_CREAT | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
        try:
            info = os.fstat(descriptor)
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid not in {OWNER_UID, SYSTEM_UID} or stat.S_IMODE(info.st_mode) != 0o600:
                raise SafetyError('Unsafe disabled marker')
        finally:
            os.close(descriptor)
        failures = []
        for (target, path), state in zip(TARGETS, states):
            if state == 'already_unloaded':
                print(target + ': already_unloaded')
                continue
            try:
                result = launchctl('bootout', target)
                if probe(target, path) != 'already_unloaded':
                    raise SafetyError('Still loaded')
                print(target + (': stopped' if result.returncode == 0 else ': already_unloaded'))
            except (SafetyError, OSError, subprocess.TimeoutExpired):
                failures.append(target)
                print(target + ': stop_failed')
        if failures:
            raise SafetyError('Beta remains disabled; stop failed: ' + ', '.join(failures))
        print('All four Beta services stopped/unloaded; production, shared Tunnel and credentials preserved.')
        return 0
    except (SafetyError, OSError, ValueError, plistlib.InvalidFileException, subprocess.TimeoutExpired) as error:
        print('Beta stop aborted: ' + (str(error) if isinstance(error, SafetyError) else type(error).__name__))
        return 1

if __name__ == '__main__':
    raise SystemExit(main())
