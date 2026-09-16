#!/usr/bin/env python3
"""Attach an existing authenticated Executor HTTP endpoint to Secure MCP Tunnel.

Uses the official tunnel-client and an owner-provided credential file. Keys are
kept out of argv and generated profiles. OAuth and MCP tool annotations remain
the responsibility of the existing Executor server; this changes connectivity
only. Run with --help for the bounded, explicit lifecycle commands.
"""

import argparse
from dataclasses import dataclass, field
import ipaddress
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
from urllib.parse import urlsplit


ALIAS = 'executor-openai-tunnel'
MAX_CREDENTIAL_BYTES = 16384
KEY_PATTERN = re.compile(r'(?<![A-Za-z0-9_-])sk-[A-Za-z0-9_-]{20,}(?![A-Za-z0-9_-])')
ID_PATTERN = re.compile(r'(?<![A-Za-z0-9_])tunnel_[0-9a-f]{32}(?![A-Za-z0-9_])')


class TunnelError(Exception):
    """An actionable diagnostic that contains no credentials."""


@dataclass(frozen=True)
class Credentials:
    tunnel_id: str
    api_key: str = field(repr=False)


def redact(text: str) -> str:
    """Defense in depth for subprocess diagnostics; raw HTTP logging stays off."""
    return KEY_PATTERN.sub('[REDACTED]', text)


def load_credentials(path: Path) -> Credentials:
    """Parse text/JSON/env-style input as data, with bounded no-follow reads."""
    if not hasattr(os, 'O_NOFOLLOW') or not hasattr(os, 'getuid'):
        raise TunnelError('Credential-file mode is supported on macOS and Linux only.')
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, 'rb') as stream:
            metadata = os.fstat(stream.fileno())
            if not stat.S_ISREG(metadata.st_mode):
                raise TunnelError('Credentials must be a regular file.')
            if metadata.st_uid != os.getuid():
                raise TunnelError('Credentials must be owned by the current user.')
            if metadata.st_mode & 0o077:
                raise TunnelError('Credentials require owner-only permissions (chmod 600).')
            if metadata.st_size > MAX_CREDENTIAL_BYTES:
                raise TunnelError('Credential file exceeds the 16 KiB limit.')
            raw = stream.read(MAX_CREDENTIAL_BYTES + 1)
        if len(raw) > MAX_CREDENTIAL_BYTES:
            raise TunnelError('Credential file exceeds the 16 KiB limit.')
        text = raw.decode('utf-8-sig')
    except (OSError, UnicodeError) as exc:
        raise TunnelError('Unable to read the owner-only regular credential file.') from None
    identifiers = set(ID_PATTERN.findall(text))
    keys = set(KEY_PATTERN.findall(text))
    if len(identifiers) != 1 or len(keys) != 1:
        raise TunnelError('Credentials must contain exactly one tunnel ID and one runtime API key.')
    key = next(iter(keys))
    if key.startswith('sk-admin-'):
        raise TunnelError('Use a runtime API key with Tunnels Read and Use permissions.')
    return Credentials(next(iter(identifiers)), key)


def validate_mcp_url(value: str) -> str:
    try:
        parsed = urlsplit(value)
        valid = (
            parsed.hostname is not None
            and '*' not in value
            and not any(c.isspace() for c in value)
            and (parsed.port is None or 0 < parsed.port < 65536)
            and (parsed.scheme == 'https' or (
                parsed.scheme == 'http' and parsed.port is not None
                and ipaddress.ip_address(parsed.hostname).is_loopback))
            and parsed.path == '/mcp'
            and parsed.username is None
            and parsed.password is None
            and not parsed.query
            and not parsed.fragment
        )
    except (ValueError, TypeError):
        valid = False
    if not valid:
        raise TunnelError('MCP URL must be HTTPS or explicit loopback HTTP, with path /mcp and no embedded credentials.')
    return value


def client_environment(credentials: Credentials | None = None) -> dict[str, str]:
    # Do not inherit other accounts, endpoints, profiles, proxies or raw logging.
    allowed = ('PATH', 'HOME', 'USER', 'LOGNAME', 'LANG', 'LC_ALL', 'LC_CTYPE',
               'TMPDIR', 'SYSTEMROOT', 'SSL_CERT_FILE', 'SSL_CERT_DIR')
    result = {name: os.environ[name] for name in allowed if name in os.environ}
    if credentials is not None:
        result['CONTROL_PLANE_API_KEY'] = credentials.api_key
    return result


def invoke(client: str, arguments: list[str], credentials: Credentials | None = None,
           timeout: int = 45) -> str:
    try:
        result = subprocess.run(
            [client, *arguments], env=client_environment(credentials),
            stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=timeout,
            check=False,
        )
    except subprocess.TimeoutExpired:
        raise TunnelError('Client timed out; outcome is unconfirmed. Inspect runtime status before retrying.') from None
    except OSError:
        raise TunnelError('Unable to execute the installed official tunnel-client.') from None
    output = redact(result.stdout)
    diagnostic = redact(result.stderr)
    if credentials is not None:
        output = output.replace(credentials.api_key, '[REDACTED]')
        diagnostic = diagnostic.replace(credentials.api_key, '[REDACTED]')
    if result.returncode:
        detail = (diagnostic or output).strip()[:5000]
        raise TunnelError(f'Client exited {result.returncode}: {detail}')
    return output


def parse_json(text: str) -> dict:
    try:
        value = json.loads(text)
    except (ValueError, TypeError):
        raise TunnelError('Client returned an invalid JSON result.') from None
    if not isinstance(value, dict):
        raise TunnelError('Client returned an invalid JSON object.')
    return value


def assert_unused(inventory: dict, tunnel_id: str) -> None:
    aliases = inventory.get('aliases')
    if not isinstance(aliases, list):
        raise TunnelError('Runtime inventory is unavailable; refusing to attach.')
    for entry in aliases:
        if not isinstance(entry, dict):
            raise TunnelError('Runtime inventory is malformed; refusing to attach.')
        if entry.get('alias') == ALIAS or entry.get('tunnel_id') == tunnel_id:
            raise TunnelError('Tunnel ID or Executor alias is already registered; inspect its status before changing it.')


def connect_arguments(credentials: Credentials, mcp_url: str, profile_dir: Path) -> list[str]:
    return [
        'runtimes', 'connect', '--alias', ALIAS,
        '--tunnel-id', credentials.tunnel_id,
        '--profile', ALIAS, '--profile-dir', str(profile_dir),
        '--runtime-api-key', 'env:CONTROL_PLANE_API_KEY',
        '--mcp-server-url', validate_mcp_url(mcp_url),
        '--control-plane-base-url', 'https://api.openai.com', '--json',
    ]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('check', 'inspect', 'doctor', 'connect', 'status'))
    parser.add_argument('--credentials', type=Path,
                        help='Existing owner-only file containing a tunnel ID and runtime API key')
    parser.add_argument('--mcp-url', help='Existing authenticated HTTPS or loopback HTTP /mcp endpoint')
    parser.add_argument('--profile-dir', type=Path, default=
                        Path.home() / 'Library/Application Support/Executor OpenAI Tunnel/profiles'
                        if sys.platform == 'darwin' else
                        Path.home() / '.config/executor-openai-tunnel/profiles')
    options = parser.parse_args(argv)
    try:
        if options.action == 'status':
            credentials = None
        else:
            if options.credentials is None:
                raise TunnelError('--credentials is required for this action.')
            credentials = load_credentials(options.credentials)
        if options.action == 'check':
            print(json.dumps({'credentials': 'validated', 'tunnel_id': credentials.tunnel_id,
                              'key': 'present', 'permissions': 'owner-only'}))
            return 0
        client = shutil.which('tunnel-client')
        if not client:
            raise TunnelError('Install the official tunnel-client before continuing.')
        if options.action == 'inspect':
            print(invoke(client, ['admin', 'tunnels', 'get', credentials.tunnel_id,
                                  '--control-plane.base-url', 'https://api.openai.com', '--json'],
                         credentials).strip())
        elif options.action == 'status':
            print(invoke(client, ['runtimes', 'status', ALIAS, '--json']).strip())
        else:
            if not options.mcp_url:
                raise TunnelError('--mcp-url is required for doctor/connect.')
            mcp_url = validate_mcp_url(options.mcp_url)
            if options.action == 'doctor':
                print(invoke(client, [
                    'doctor', '--control-plane.tunnel-id', credentials.tunnel_id,
                    '--control-plane.api-key', 'env:CONTROL_PLANE_API_KEY',
                    '--control-plane.base-url', 'https://api.openai.com',
                    '--mcp.server-url', mcp_url, '--health.listen-addr', '127.0.0.1:0',
                    '--json', '--explain',
                ], credentials).strip())
            else:
                inventory = parse_json(invoke(client, ['runtimes', 'list', '--json']))
                assert_unused(inventory, credentials.tunnel_id)
                # The official client writes only the env reference into this profile.
                previous_mask = os.umask(0o077)
                try:
                    print(invoke(client, connect_arguments(credentials, mcp_url,
                                                           options.profile_dir.absolute()),
                                 credentials).strip())
                finally:
                    os.umask(previous_mask)
                print(invoke(client, ['runtimes', 'status', ALIAS, '--json']).strip())
        return 0
    except TunnelError as exc:
        print(str(exc), file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
