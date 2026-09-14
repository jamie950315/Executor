"""Bounded independent Dashboard-only fault/recovery guard. Never invokes Beta stop-all.

prepare creates private fixtures. watch waits for stop.request, stops only Dashboard,
restores in finally, then waits for complete before deleting only its own fixtures.
The browser reader must use a separate local control context and a bounded fetch.
"""
import argparse
import errno
import json
import os
from pathlib import Path
import signal
import stat
import tempfile
import time
import beta_stop

NAMES = {'normal.txt', 'empty.txt', 'wait.fifo', 'owner.json', 'ready', 'stop.request', 'complete'}


def create_fixture():
    directory = Path(tempfile.mkdtemp(prefix='executor-beta-final-', dir='/private/tmp'))
    (directory / 'normal.txt').write_text('Executor Beta finalization 正常讀取 ✅\n')
    (directory / 'empty.txt').write_bytes(b'')
    os.mkfifo(directory / 'wait.fifo', 0o600)
    for name in ('normal.txt', 'empty.txt'):
        (directory / name).chmod(0o600)
    identities = {name: [p.lstat().st_dev, p.lstat().st_ino] for name in ('normal.txt', 'empty.txt', 'wait.fifo')
                  for p in [directory / name]}
    (directory / 'owner.json').write_text(json.dumps(identities))
    (directory / 'owner.json').chmod(0o600)
    return directory


def validate_fixture(directory):
    if directory.parent != Path('/private/tmp') or not directory.name.startswith('executor-beta-final-'):
        raise RuntimeError('Unexpected fixture location')
    info = directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != 501 or stat.S_IMODE(info.st_mode) != 0o700:
        raise RuntimeError('Untrusted fixture directory')
    children = list(directory.iterdir())
    if any(p.name not in NAMES or p.is_symlink() or p.lstat().st_uid != 501 for p in children):
        raise RuntimeError('Untrusted fixture contents')
    identities = json.loads((directory / 'owner.json').read_bytes())
    if set(identities) != {'normal.txt', 'empty.txt', 'wait.fifo'}:
        raise RuntimeError('Invalid fixture ownership record')
    for name, identity in identities.items():
        info = (directory / name).lstat()
        expected_type = stat.S_ISFIFO(info.st_mode) if name == 'wait.fifo' else stat.S_ISREG(info.st_mode)
        if not expected_type or [info.st_dev, info.st_ino] != identity:
            raise RuntimeError('Fixture identity changed')


def release_fifo(directory):
    validate_fixture(directory)
    try:
        fd = os.open(directory / 'wait.fifo', os.O_WRONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    except OSError as error:
        if error.errno == errno.ENXIO:
            return 'no_waiting_reader'
        raise
    os.close(fd)
    return 'released'


def cleanup(directory):
    validate_fixture(directory)
    for child in list(directory.iterdir()):
        child.unlink()
    directory.rmdir()


def signal_fixture(directory, name):
    if name not in ('complete', 'stop.request'):
        raise RuntimeError('Unknown fixture signal')
    if directory.parent != Path('/private/tmp') or not directory.name.startswith('executor-beta-final-'):
        raise RuntimeError('Unexpected fixture location')
    try:
        descriptor = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    except FileNotFoundError:
        if name == 'complete':
            return 'already_cleaned'
        raise
    try:
        validate_fixture(directory)
        info = os.fstat(descriptor)
        current = directory.lstat()
        if (info.st_dev, info.st_ino) != (current.st_dev, current.st_ino):
            raise RuntimeError('Fixture directory changed')
        try:
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=descriptor)
        except FileExistsError:
            return 'already_signalled'
        try:
            os.write(fd, b'fixture-signal\n')
        finally:
            os.close(fd)
        return 'signalled'
    except FileNotFoundError:
        if name == 'complete':
            return 'already_cleaned'
        raise
    finally:
        os.close(descriptor)


def restore_dashboard():
    beta_stop.validate()
    target, plist = beta_stop.TARGETS[0]
    if target != 'gui/501/com.executor.beta.dashboard':
        raise RuntimeError('Unexpected recovery target')
    if beta_stop.probe(target, plist) == 'already_unloaded':
        result = beta_stop.launchctl('bootstrap', 'gui/501', str(plist))
        if result.returncode:
            raise RuntimeError('Dashboard restoration failed')
    if beta_stop.probe(target, plist) != 'loaded':
        raise RuntimeError('Dashboard not loaded after recovery')
    return 'loaded'


def watch(directory, report_dir, stop_wait=90, complete_wait=90, offline_seconds=5):
    validate_fixture(directory)
    beta_stop.checked_stat(report_dir, 501, directory=True)
    identities = beta_stop.validate()
    target, plist = beta_stop.TARGETS[0]
    if beta_stop.probe(target, plist) != 'loaded':
        raise RuntimeError('Dashboard must be loaded before this test')
    result, stop_attempted = {'pid': os.getpid(), 'target': target}, False
    (directory / 'ready').write_text('ready')
    print(json.dumps({'ready': str(directory), 'pid': os.getpid()}), flush=True)
    try:
        deadline = time.monotonic() + stop_wait
        while not (directory / 'stop.request').exists():
            if (directory / 'complete').exists():
                return
            if time.monotonic() >= deadline:
                raise TimeoutError('No fault requested before deadline')
            time.sleep(0.1)
        validate_fixture(directory)
        if beta_stop.validate() != identities:
            raise RuntimeError('Beta identity changed before fault')
        stop_attempted = True
        stopped = beta_stop.launchctl('bootout', target)
        if stopped.returncode:
            raise RuntimeError('Dashboard stop refused')
        result['stopped'] = True
        print(json.dumps({'stopped': target}), flush=True)
        time.sleep(offline_seconds)
    except Exception as error:
        result['error'] = type(error).__name__
        raise
    finally:
        # Neither this process nor cleanup depends on the relay being available.
        try:
            result['fifo'] = release_fifo(directory)
        finally:
            try:
                if stop_attempted:
                    result['restored'] = restore_dashboard()
                print(json.dumps({'recovery': result}), flush=True)
                deadline = time.monotonic() + complete_wait
                while not (directory / 'complete').exists() and time.monotonic() < deadline:
                    time.sleep(0.1)
            finally:
                try:
                    cleanup(directory)
                    result['fixtures_removed'] = True
                finally:
                    with (report_dir / 'body-guard.json').open('x') as output:
                        json.dump(result, output, indent=2)
                    (report_dir / 'body-guard.json').chmod(0o600)
                    print(json.dumps({'finished': result}), flush=True)


def cancelled(_signum, _frame):
    raise RuntimeError('Guard interrupted; running recovery')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['prepare', 'watch', 'request-stop', 'complete'])
    parser.add_argument('--directory', type=Path)
    parser.add_argument('--report-dir', type=Path)
    args = parser.parse_args()
    if args.action == 'prepare':
        print(create_fixture())
    elif args.action in ('request-stop', 'complete'):
        print(signal_fixture(args.directory, 'stop.request' if args.action == 'request-stop' else 'complete'))
    else:
        signal.signal(signal.SIGTERM, cancelled)
        signal.signal(signal.SIGINT, cancelled)
        watch(args.directory, args.report_dir)
