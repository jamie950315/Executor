"""Read-only metadata inventory; never emits config, credentials or launchctl output."""
import argparse
import hashlib
import json
from pathlib import Path
import plistlib
import re
import subprocess


def snapshot():
    files, services = {}, {}
    def fingerprint(path):
        path = Path(path)
        info = path.stat()
        files[str(path)] = {'sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
                            'uid': info.st_uid, 'gid': info.st_gid, 'mode': oct(info.st_mode & 0o777)}
    specs = []
    for folder, domain in [(Path('/Library/LaunchDaemons'), 'system'),
                           (Path('/Library/LaunchAgents'), 'gui/501'),
                           (Path('/Users/jamie/Library/LaunchAgents'), 'gui/501')]:
        for path in sorted(folder.glob('com.executor.*.plist')):
            specs.append((path, domain))
    for path, domain in specs:
        doc = plistlib.loads(path.read_bytes())
        label, argv = doc['Label'], doc['ProgramArguments']
        target = domain + '/' + label
        result = subprocess.run(['/bin/launchctl', 'print', target], capture_output=True, text=True, timeout=10)
        if result.returncode:
            raise RuntimeError('Service inventory unavailable: ' + target)
        match = re.search(r'^\s*pid = (\d+)\s*$', result.stdout, re.M)
        if not match:
            raise RuntimeError('Service PID unavailable: ' + target)
        services[target] = int(match.group(1))
        fingerprint(path)
        fingerprint(argv[0])
        for flag in ('--config', '--token-file'):
            if flag in argv:
                config_path = Path(argv[argv.index(flag) + 1])
                fingerprint(config_path)
                if flag == '--config' and config_path.suffix == '.json':
                    cfg = json.loads(config_path.read_bytes())
                    state = Path(cfg['state_dir'])
                    for name in ('secrets.json', 'oauth-state.json'):
                        if (state / name).exists():
                            fingerprint(state / name)
    base = Path('/Users/jamie/Library/Application Support/Executor Beta')
    fingerprint(base / 'stop.py')
    marker = base / 'state/disabled'
    return {'services': services, 'files': files, 'beta_disabled_marker': marker.exists()}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    data = snapshot()
    if args.output:
        with args.output.open('x') as output:
            json.dump(data, output, indent=2)
        args.output.chmod(0o600)
        print(json.dumps({'services': len(data['services']), 'files': len(data['files']),
                          'beta_disabled_marker': data['beta_disabled_marker']}))
    else:
        print(json.dumps(data, indent=2))
