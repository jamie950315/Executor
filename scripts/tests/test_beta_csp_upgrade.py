import subprocess
import sys
from pathlib import Path
import unittest

class CSPUpgradeTests(unittest.TestCase):
    def test_csp_update_preserves_alias_and_scopes_stop_helper(self):
        # Isolate the one-shot script's pinned module overrides from other tests.
        script = '''
import beta_csp_upgrade as c
u = c.upgrade
cfg = {'domain': u.DOMAIN, 'agent_address': '127.0.0.1:18787', 'state_dir': str(u.STATE), 'oauth_resource_aliases': [u.ALIAS]}
plist = {'Label': u.LABEL, 'ProgramArguments': [str(u.OLD), 'agent', '--config', str(u.CONFIG)], 'UserName': 'jamie'}
new_cfg, new_plist = c.plan(cfg, plist)
assert new_cfg == cfg
assert new_plist['ProgramArguments'][0] == str(u.NEW)
old = "return [str(BASE / ('bundle-tunnel-1cc79b1' if role == 'agent' else 'bundle-ec5f53d') / relative)]"
result = c.update_stop(old)
assert "'bundle-tunnel-csp' if role == 'agent'" in result
assert "else 'bundle-ec5f53d'" in result
for bad in ({}, dict(cfg, oauth_resource_aliases=['https://other.example/mcp'])):
    try:
        c.plan(bad, plist)
    except ValueError:
        pass
    else:
        raise AssertionError('unexpected config accepted')
'''
        result = subprocess.run([sys.executable, '-B', '-c', script],
                                cwd=Path(__file__).parents[1], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)

if __name__ == '__main__':
    unittest.main()
