import importlib.util
from pathlib import Path
import unittest
from unittest import mock
import contextlib
import io
import json
import os
import plistlib
import subprocess
import tempfile
import sys

SPEC = importlib.util.spec_from_file_location('beta_stop', Path(__file__).parents[1] / 'beta_stop.py')
stop = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = stop
SPEC.loader.exec_module(stop)
INSTALL_SPEC = importlib.util.spec_from_file_location('install_beta_stop', Path(__file__).parents[1] / 'install_beta_stop.py')
installer = importlib.util.module_from_spec(INSTALL_SPEC)
INSTALL_SPEC.loader.exec_module(installer)


class TargetRegression(unittest.TestCase):
    def test_all_four_exact_targets_in_safe_order(self):
        self.assertEqual([t for t, _ in stop.TARGETS], [
            'gui/501/com.executor.beta.dashboard',
            'gui/501/com.executor.beta.desktop',
            'system/com.executor.beta.agent',
            'system/com.executor.beta.broker',
        ])


class FixtureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='.beta-stop-', dir=Path(__file__).resolve().parent)
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        base, user, system = root / 'Beta', root / 'LaunchAgents', root / 'LaunchDaemons'
        for p in (base / 'state', user, system):
            p.mkdir(parents=True, mode=0o700)
        targets = tuple((target, (user if role in ('desktop', 'dashboard') else system) / path.name)
                        for role, (target, path) in zip(stop.ROLES, stop.TARGETS))
        patch = mock.patch.multiple(stop, BASE=base, STATE=base / 'state', USER_PLISTS=user,
                                    SYSTEM_PLISTS=system, SYSTEM_UID=os.getuid(), TARGETS=targets)
        patch.start()
        self.addCleanup(patch.stop)
        self.config = {'domain': 'beta-executor-mac.0ruka.dev', 'state_dir': str(stop.STATE),
                       'agent_address': '127.0.0.1:18787', 'dashboard_address': '127.0.0.1:18788',
                       'broker_endpoint': str(stop.STATE / 'broker.sock'), 'desktop_endpoint': str(stop.STATE / 'desktop.sock')}
        self.write(stop.STATE / 'config.json', json.dumps(self.config).encode())
        self.write(stop.STATE / 'secrets.json', b'fixture-secret-not-a-real-key')
        for role, (_, path) in zip(stop.ROLES, stop.TARGETS):
            argv = stop.expected_argv(role)
            binary = Path(argv[0])
            binary.parent.mkdir(parents=True, exist_ok=True)
            self.write(binary, b'fixture executable', 0o700)
            doc = {'Label': 'com.executor.beta.' + role, 'ProgramArguments': argv}
            if role in ('agent', 'broker'):
                doc['UserName'] = 'root' if role == 'broker' else 'jamie'
            self.write(path, plistlib.dumps(doc))
        self.loaded = {target: True for target, _ in targets}
        self.calls, self.fail_stop, self.deny_probe = [], set(), set()
        self.loaded_wrong = False
        fake = mock.patch.object(stop.subprocess, 'run', side_effect=self.fake_launchctl)
        fake.start()
        self.addCleanup(fake.stop)
        root_user = mock.patch.object(stop.os, 'geteuid', return_value=0)
        root_user.start()
        self.addCleanup(root_user.stop)

    def write(self, path, data, mode=0o600):
        path.write_bytes(data)
        path.chmod(mode)

    def fake_launchctl(self, args, **kwargs):
        self.assertEqual(args[0], '/bin/launchctl')
        self.assertEqual(kwargs['timeout'], 10)
        action, target = args[1:]
        self.assertIn(target, self.loaded, 'No production/shared target may be called')
        self.calls.append((action, target))
        path = dict(stop.TARGETS)[target]
        if action == 'print':
            if target in self.deny_probe:
                return subprocess.CompletedProcess(args, 1, '', 'Not permitted')
            if not self.loaded[target]:
                return subprocess.CompletedProcess(args, 113, '', f'Could not find service "{target.rsplit("/", 1)[1]}" in domain for user')
            role = target.rsplit('.', 1)[1]
            argv = stop.expected_argv(role)
            program = '/production/executor' if self.loaded_wrong else argv[0]
            out = f'{target} = {{\n path = {path}\n program = {program}\n arguments = {{\n' + '\n'.join(argv) + '\n }\n pid = 123\n}\n'
            return subprocess.CompletedProcess(args, 0, out, '')
        self.assertEqual(action, 'bootout')
        if target in self.fail_stop:
            return subprocess.CompletedProcess(args, 5, '', 'stop failed')
        self.loaded[target] = False
        return subprocess.CompletedProcess(args, 0, '', '')

    def run_stop(self, check=False):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = stop.main(['--check'] if check else [])
        return code, out.getvalue()

    def assert_no_mutation(self):
        self.assertFalse((stop.STATE / 'disabled').exists())
        self.assertFalse(any(action == 'bootout' for action, _ in self.calls))

    def change_plist(self, role, key, value):
        path = next(path for target, path in stop.TARGETS if target.endswith('.' + role))
        doc = plistlib.loads(path.read_bytes())
        doc[key] = value
        self.write(path, plistlib.dumps(doc))

    def test_check_all_four_is_read_only(self):
        before = {p: p.read_bytes() for p in stop.STATE.iterdir()}
        code, output = self.run_stop(check=True)
        self.assertEqual(code, 0, output)
        self.assertEqual(self.calls, [('print', t) for t, _ in stop.TARGETS])
        self.assertEqual(before, {p: p.read_bytes() for p in stop.STATE.iterdir()})
        self.assertTrue(all(self.loaded.values()))
        self.assert_no_mutation()

    def test_stop_exact_four_in_safe_order(self):
        secret = (stop.STATE / 'secrets.json').read_bytes()
        code, output = self.run_stop()
        self.assertEqual(code, 0, output)
        self.assertEqual([t for a, t in self.calls if a == 'bootout'], [t for t, _ in stop.TARGETS])
        self.assertTrue((stop.STATE / 'disabled').is_file())
        self.assertEqual((stop.STATE / 'secrets.json').read_bytes(), secret)

    def test_already_unloaded_is_explicit(self):
        self.loaded[stop.TARGETS[0][0]] = False
        code, out = self.run_stop()
        self.assertEqual(code, 0, out)
        self.assertIn('dashboard: already_unloaded', out)
        self.assertNotIn(('bootout', stop.TARGETS[0][0]), self.calls)

    def test_failure_reports_target_and_preserves_marker(self):
        self.fail_stop.add(stop.TARGETS[0][0])
        code, out = self.run_stop()
        self.assertEqual(code, 1)
        self.assertIn('dashboard: stop_failed', out)
        self.assertTrue((stop.STATE / 'disabled').exists())

    def test_denied_probe_aborts_before_marker_or_stop(self):
        self.deny_probe.add(stop.TARGETS[-1][0])
        self.assertEqual(self.run_stop()[0], 1)
        self.assert_no_mutation()

    def test_loaded_identity_mismatch_aborts(self):
        self.loaded_wrong = True
        self.assertEqual(self.run_stop()[0], 1)
        self.assert_no_mutation()

    def test_wrong_last_label_prevents_every_stop(self):
        self.change_plist('broker', 'Label', 'com.executor.broker')
        self.assertEqual(self.run_stop()[0], 1)
        self.assertEqual(self.calls, [])
        self.assert_no_mutation()

    def test_wrong_role_binary_and_config_are_rejected(self):
        good = stop.expected_argv('dashboard')
        for index, value in [(0, '/production/executor'), (1, 'agent'), (3, '/var/lib/executor/config.json')]:
            with self.subTest(index=index):
                argv = good.copy()
                argv[index] = value
                self.change_plist('dashboard', 'ProgramArguments', argv)
                self.assertEqual(self.run_stop()[0], 1)
                self.assert_no_mutation()
        self.change_plist('dashboard', 'ProgramArguments', good)

    def test_wrong_state_and_environment_are_rejected(self):
        self.change_plist('dashboard', 'EnvironmentVariables', {'EXECUTOR_STATE_DIR': '/var/lib/executor'})
        self.assertEqual(self.run_stop()[0], 1)
        self.assert_no_mutation()

    def test_symlink_plist_rejected(self):
        path = stop.TARGETS[-1][1]
        real = path.with_suffix('.real')
        path.rename(real)
        path.symlink_to(real)
        self.assertEqual(self.run_stop()[0], 1)
        self.assert_no_mutation()

    def test_symlink_ancestor_rejected(self):
        bundle = stop.BASE / 'bundle-ec5f53d'
        real = stop.BASE / 'real-bundle'
        bundle.rename(real)
        bundle.symlink_to(real, target_is_directory=True)
        self.assertEqual(self.run_stop()[0], 1)
        self.assert_no_mutation()

    def test_untrusted_owner_rejected(self):
        with mock.patch.object(stop, 'SYSTEM_UID', os.getuid() + 1):
            self.assertEqual(self.run_stop()[0], 1)
        self.assert_no_mutation()

    def test_existing_marker_not_truncated(self):
        marker = stop.STATE / 'disabled'
        self.write(marker, b'keep marker meaning')
        inode = marker.stat().st_ino
        self.assertEqual(self.run_stop(check=True)[0], 0)
        self.assertEqual(self.run_stop()[0], 0)
        self.assertEqual(marker.read_bytes(), b'keep marker meaning')
        self.assertEqual(marker.stat().st_ino, inode)

    def test_marker_symlink_rejected(self):
        (stop.STATE / 'disabled').symlink_to(stop.STATE / 'secrets.json')
        self.assertEqual(self.run_stop()[0], 1)
        self.assertEqual(self.calls, [])

    def test_nonroot_stop_does_not_write_marker(self):
        with mock.patch.object(stop.os, 'geteuid', return_value=501):
            self.assertEqual(self.run_stop()[0], 1)
        self.assert_no_mutation()

    def test_world_writable_plist_rejected(self):
        stop.TARGETS[-1][1].chmod(0o666)
        self.assertEqual(self.run_stop()[0], 1)
        self.assert_no_mutation()

    def test_probe_timeout_is_not_treated_as_unloaded(self):
        with mock.patch.object(stop.subprocess, 'run', side_effect=subprocess.TimeoutExpired('launchctl', 10)):
            self.assertEqual(self.run_stop()[0], 1)
        self.assert_no_mutation()

    def test_atomic_install_backs_up_and_preserves_owner_mode(self):
        target, source = stop.BASE / 'stop.py', stop.BASE / 'source.py'
        self.write(target, b'# original\n', 0o640)
        self.write(source, b'# tested replacement\n', 0o600)
        before = target.stat()
        result = installer.install(source, stop.BASE / 'backup')
        self.assertEqual(target.read_bytes(), source.read_bytes())
        self.assertEqual((stop.BASE / 'backup/stop.py').read_bytes(), b'# original\n')
        self.assertEqual(target.stat().st_mode & 0o777, 0o640)
        self.assertEqual((target.stat().st_uid, target.stat().st_gid), (before.st_uid, before.st_gid))
        self.assertNotEqual(target.stat().st_ino, before.st_ino)
        self.assertEqual(result['mode'], 0o640)
        self.assertEqual(list(stop.BASE.glob('.stop-install-*')), [])
        self.assertEqual(self.calls, [])

    def test_install_refuses_symlink_target(self):
        target, source = stop.BASE / 'stop.py', stop.BASE / 'source.py'
        self.write(source, b'# replacement\n')
        target.symlink_to(source)
        with self.assertRaises(stop.SafetyError):
            installer.install(source, stop.BASE / 'backup')
        self.assertFalse((stop.BASE / 'backup').exists())

    def test_install_refuses_invalid_python_before_backup(self):
        self.write(stop.BASE / 'stop.py', b'# original\n')
        self.write(stop.BASE / 'source.py', b'def invalid(\n')
        with self.assertRaises(SyntaxError):
            installer.install(stop.BASE / 'source.py', stop.BASE / 'backup')
        self.assertFalse((stop.BASE / 'backup').exists())


if __name__ == '__main__':
    unittest.main()
