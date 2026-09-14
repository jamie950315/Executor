import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock
from test_beta_stop import stop

spec = importlib.util.spec_from_file_location('beta_body_guard', Path(__file__).parents[1] / 'beta_body_guard.py')
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


class GuardTests(unittest.TestCase):
    def setUp(self):
        self.directory = guard.create_fixture()
        self.report = tempfile.TemporaryDirectory(dir=Path(__file__).resolve().parent)
        self.addCleanup(self.report.cleanup)
        self.addCleanup(lambda: guard.cleanup(self.directory) if self.directory.exists() else None)
        self.loaded, self.calls = True, []

    def probe(self, target, path):
        self.assertEqual(target, 'gui/501/com.executor.beta.dashboard')
        self.assertEqual(path, stop.TARGETS[0][1])
        return 'loaded' if self.loaded else 'already_unloaded'

    def run_launchctl(self, *args):
        self.calls.append(args)
        if args[0] == 'bootout':
            self.assertEqual(args, ('bootout', 'gui/501/com.executor.beta.dashboard'))
            self.loaded = False
        else:
            self.assertEqual(args, ('bootstrap', 'gui/501', str(stop.TARGETS[0][1])))
            self.loaded = True
        return subprocess.CompletedProcess(args, 0, '', '')

    def watch(self):
        with mock.patch.object(stop, 'validate', return_value={}), \
             mock.patch.object(stop, 'checked_stat'), \
             mock.patch.object(stop, 'probe', side_effect=self.probe), \
             mock.patch.object(stop, 'launchctl', side_effect=self.run_launchctl), \
             contextlib.redirect_stdout(io.StringIO()):
            guard.watch(self.directory, Path(self.report.name), stop_wait=0, complete_wait=0, offline_seconds=0)

    def test_dashboard_only_stop_restore_and_cleanup(self):
        (self.directory / 'stop.request').touch()
        self.watch()
        self.assertEqual(len(self.calls), 2)
        self.assertTrue(self.loaded)
        self.assertFalse(self.directory.exists())
        report = json.loads((Path(self.report.name) / 'body-guard.json').read_bytes())
        self.assertEqual(report['restored'], 'loaded')
        self.assertTrue(report['fixtures_removed'])

    def test_timeout_without_request_never_stops_and_cleans(self):
        with self.assertRaises(TimeoutError):
            self.watch()
        self.assertEqual(self.calls, [])
        self.assertFalse(self.directory.exists())

    def test_no_reader_fifo_release_is_nonblocking(self):
        self.assertEqual(guard.release_fifo(self.directory), 'no_waiting_reader')

    def test_unknown_fixture_file_is_not_deleted(self):
        extra = self.directory / 'not-our-data'
        extra.write_text('preserve')
        with self.assertRaises(RuntimeError):
            guard.cleanup(self.directory)
        self.assertEqual(extra.read_text(), 'preserve')
        extra.unlink()


if __name__ == '__main__':
    unittest.main()
