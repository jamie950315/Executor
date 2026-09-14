"""Back up and atomically replace only the installed Beta stop.py."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import tempfile
import beta_stop


def install(source, backup):
    target = beta_stop.BASE / 'stop.py'
    backup = Path(backup)
    if not backup.is_relative_to(beta_stop.BASE) or backup.exists():
        raise beta_stop.SafetyError('Backup must be a new private directory inside Beta')
    beta_stop.checked_stat(backup.parent, beta_stop.OWNER_UID, directory=True)
    original, identity = beta_stop.checked_read(target, beta_stop.OWNER_UID)
    replacement, _ = beta_stop.checked_read(source, beta_stop.OWNER_UID)
    compile(replacement, str(source), 'exec')
    info = target.stat()
    backup.mkdir(mode=0o700)
    old_path = backup / 'stop.py'
    fd = os.open(old_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as output:
        output.write(original)
        output.flush()
        os.fchown(output.fileno(), info.st_uid, info.st_gid)
        os.fchmod(output.fileno(), stat.S_IMODE(info.st_mode))
        os.fsync(output.fileno())
    metadata = {'target': str(target), 'source': str(source), 'uid': info.st_uid, 'gid': info.st_gid,
                'mode': stat.S_IMODE(info.st_mode), 'old_sha256': hashlib.sha256(original).hexdigest(),
                'new_sha256': hashlib.sha256(replacement).hexdigest()}
    with (backup / 'metadata.json').open('x') as output:
        json.dump(metadata, output, indent=2)
    (backup / 'metadata.json').chmod(0o600)
    fd, pending = tempfile.mkstemp(prefix='.stop-install-', dir=beta_stop.BASE)
    try:
        with os.fdopen(fd, 'wb') as output:
            output.write(replacement)
            output.flush()
            os.fchown(output.fileno(), info.st_uid, info.st_gid)
            os.fchmod(output.fileno(), stat.S_IMODE(info.st_mode))
            os.fsync(output.fileno())
        if beta_stop.checked_read(target, beta_stop.OWNER_UID)[1] != identity:
            raise beta_stop.SafetyError('Installed stop.py changed; preserving concurrent work')
        os.replace(pending, target)
        directory = os.open(beta_stop.BASE, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(pending):
            os.unlink(pending)
    if hashlib.sha256(target.read_bytes()).hexdigest() != metadata['new_sha256']:
        raise beta_stop.SafetyError('Installed hash mismatch')
    return metadata


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--backup-dir', required=True, type=Path)
    parser.add_argument('--restore-from', type=Path, help='Explicit rollback using a validated backup directory')
    args = parser.parse_args()
    source = Path(__file__).with_name('beta_stop.py')
    if args.restore_from:
        folder = args.restore_from
        if not folder.is_relative_to(beta_stop.BASE):
            raise SystemExit('Restore source must remain inside Beta')
        record = json.loads(beta_stop.checked_read(folder / 'metadata.json', beta_stop.OWNER_UID)[0])
        source = folder / 'stop.py'
        if record['target'] != str(beta_stop.BASE / 'stop.py') or hashlib.sha256(beta_stop.checked_read(source, beta_stop.OWNER_UID)[0]).hexdigest() != record['old_sha256']:
            raise SystemExit('Backup identity mismatch')
    print(json.dumps(install(source, args.backup_dir), indent=2))
