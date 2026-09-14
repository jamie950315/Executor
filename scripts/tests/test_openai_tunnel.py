import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location(
    'openai_tunnel', Path(__file__).parents[1] / 'openai_tunnel.py')
tunnel = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = tunnel
spec.loader.exec_module(tunnel)

TUNNEL = 'tunnel_' + 'a' * 32
KEY = 'sk-proj-' + 'test_only_not_a_real_key_' * 3


class TunnelTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / 'credentials'
        self.path.write_text(f'Tunnel ID: {TUNNEL}\nAPI Key: {KEY}\n')
        self.path.chmod(0o600)

    def test_labelled_credentials(self):
        creds = tunnel.load_credentials(self.path)
        self.assertEqual(creds.tunnel_id, TUNNEL)
        self.assertEqual(creds.api_key, KEY)
        self.assertNotIn(KEY, repr(creds))

    def test_json_credentials(self):
        self.path.write_text(json.dumps({'tunnel_id': TUNNEL, 'api_key': KEY}))
        self.assertEqual(tunnel.load_credentials(self.path).api_key, KEY)

    def test_environment_style_is_parsed_without_execution(self):
        self.path.write_text(f'CONTROL_PLANE_TUNNEL_ID={TUNNEL}\nCONTROL_PLANE_API_KEY={KEY}\n')
        self.assertEqual(tunnel.load_credentials(self.path).tunnel_id, TUNNEL)

    def test_publicly_readable_credentials_rejected(self):
        self.path.chmod(0o644)
        with self.assertRaises(tunnel.TunnelError):
            tunnel.load_credentials(self.path)

    def test_symlink_rejected(self):
        alias = self.path.with_name('link')
        alias.symlink_to(self.path)
        with self.assertRaises(tunnel.TunnelError):
            tunnel.load_credentials(alias)

    def test_directory_rejected(self):
        with self.assertRaises(tunnel.TunnelError):
            tunnel.load_credentials(self.path.parent)

    def test_oversized_credentials_rejected(self):
        self.path.write_bytes(b'x' * 16385)
        with self.assertRaises(tunnel.TunnelError):
            tunnel.load_credentials(self.path)

    def test_invalid_and_ambiguous_credentials_rejected_without_echo(self):
        for text in (KEY, TUNNEL, TUNNEL + '\n' + KEY + '\nsk-' + 'z' * 32,
                     TUNNEL + '\n' + 'sk-admin-' + 'z' * 40,
                     TUNNEL + '\n' + KEY + '\ntunnel_' + 'b' * 32):
            with self.subTest(text_length=len(text)):
                self.path.write_text(text)
                with self.assertRaises(tunnel.TunnelError) as caught:
                    tunnel.load_credentials(self.path)
                self.assertNotIn(KEY, str(caught.exception))

    def test_foreign_owner_rejected(self):
        with mock.patch.object(tunnel.os, 'getuid', return_value=os.getuid() + 1):
            with self.assertRaises(tunnel.TunnelError):
                tunnel.load_credentials(self.path)

    def test_only_loopback_mcp_urls(self):
        for value in ('http://127.0.0.1:19787/mcp', 'http://[::1]:19787/mcp'):
            self.assertEqual(tunnel.validate_mcp_url(value), value)
        for value in ('http://example.com/mcp', 'http://127.0.0.1:80/admin',
                      'http://user:password@127.0.0.1:80/mcp',
                      'http://127.0.0.1:80/mcp?secret=value',
                      'http://127.0.0.1:80/mcp#fragment',
                      'http://127.0.0.1:99999/mcp', 'file:///tmp/mcp'):
            with self.subTest(value=value):
                with self.assertRaises(tunnel.TunnelError):
                    tunnel.validate_mcp_url(value)

    def test_canonical_https_mcp_url(self):
        value = 'https://executor.example.test/mcp'
        self.assertEqual(tunnel.validate_mcp_url(value), value)
        for value in ('https://user:secret@example.test/mcp', 'https://example.test/mcp?key=x', 'https://*.example.test/mcp'):
            with self.assertRaises(tunnel.TunnelError):
                tunnel.validate_mcp_url(value)

    def test_environment_isolated_from_unrelated_credentials_and_overrides(self):
        creds = tunnel.load_credentials(self.path)
        with mock.patch.dict(os.environ, {
            'OPENAI_API_KEY': 'unrelated', 'OPENAI_ADMIN_KEY': 'unrelated',
            'CONTROL_PLANE_BASE_URL': 'https://other.example',
            'LOG_HTTP_RAW_UNSAFE': 'true', 'HTTPS_PROXY': 'https://other.example',
        }):
            env = tunnel.client_environment(creds)
        self.assertEqual(env['CONTROL_PLANE_API_KEY'], KEY)
        for name in ('OPENAI_API_KEY', 'OPENAI_ADMIN_KEY', 'CONTROL_PLANE_BASE_URL',
                     'LOG_HTTP_RAW_UNSAFE', 'HTTPS_PROXY'):
            self.assertNotIn(name, env)

    def test_existing_tunnel_is_never_rebound(self):
        for aliases in ([{'alias': 'another-project', 'tunnel_id': TUNNEL}],
                        [{'alias': tunnel.ALIAS, 'tunnel_id': 'tunnel_' + 'b' * 32}]):
            with self.assertRaises(tunnel.TunnelError):
                tunnel.assert_unused({'aliases': aliases}, TUNNEL)
        tunnel.assert_unused({'aliases': []}, TUNNEL)

    def test_missing_inventory_fails_closed(self):
        with self.assertRaises(tunnel.TunnelError):
            tunnel.assert_unused({}, TUNNEL)

    def test_output_redaction(self):
        self.assertNotIn(KEY, tunnel.redact('failure: ' + KEY))

    def test_child_failure_returns_redacted_error(self):
        creds = tunnel.load_credentials(self.path)
        with mock.patch.object(tunnel.subprocess, 'run', return_value=
                               subprocess.CompletedProcess([], 1, '', 'failure ' + KEY)):
            with self.assertRaises(tunnel.TunnelError) as caught:
                tunnel.invoke('/fake/client', ['doctor'], creds)
        self.assertNotIn(KEY, str(caught.exception))

    def test_timeout_is_unconfirmed(self):
        creds = tunnel.load_credentials(self.path)
        with mock.patch.object(tunnel.subprocess, 'run', side_effect=
                               subprocess.TimeoutExpired('client', 45)):
            with self.assertRaisesRegex(tunnel.TunnelError, 'unconfirmed'):
                tunnel.invoke('/fake/client', ['doctor'], creds)

    def test_runtime_command_preserves_http_auth_and_uses_key_reference(self):
        creds = tunnel.load_credentials(self.path)
        args = tunnel.connect_arguments(creds, 'http://127.0.0.1:19787/mcp', self.path.parent)
        self.assertIn('env:CONTROL_PLANE_API_KEY', args)
        self.assertNotIn(KEY, ' '.join(args))
        self.assertIn('--mcp-server-url', args)
        self.assertNotIn('--mcp-command', args)
        self.assertNotIn('--organization-id', args)
        self.assertNotIn('--workspace-id', args)


if __name__ == '__main__':
    unittest.main()
