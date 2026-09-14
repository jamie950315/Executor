import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('upgrade', Path(__file__).parents[1] / 'beta_tunnel_upgrade.py')
upgrade = importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade)

class UpgradeTests(unittest.TestCase):
    def test_updates_only_agent_and_exact_alias(self):
        cfg = {'domain': upgrade.DOMAIN, 'agent_address': '127.0.0.1:18787', 'state_dir': str(upgrade.STATE), 'preserve': 'value'}
        plist = {'Label': upgrade.LABEL, 'ProgramArguments': [str(upgrade.OLD), 'agent', '--config', str(upgrade.CONFIG)], 'UserName': 'jamie'}
        new_cfg, new_plist = upgrade.planned_config(cfg, plist)
        self.assertEqual(new_cfg['preserve'], 'value')
        self.assertNotIn('oauth_resource_aliases', cfg)
        self.assertEqual(new_cfg['oauth_resource_aliases'], [upgrade.ALIAS])
        self.assertEqual(new_plist['ProgramArguments'][0], str(upgrade.NEW))
        self.assertEqual(plist['ProgramArguments'][0], str(upgrade.OLD))

    def test_wrong_target_is_rejected(self):
        for cfg, plist in [({}, {}), ({'domain': 'executor-mac.0ruka.dev'}, {'Label': upgrade.LABEL})]:
            with self.assertRaises(ValueError):
                upgrade.planned_config(cfg, plist)

    def test_stop_helper_change_is_scoped(self):
        original = "def expected_argv(role):\n    " + upgrade.STOP_OLD + "\n"
        updated = upgrade.updated_stop(original)
        self.assertIn("role == 'agent'", updated)
        self.assertIn("else 'bundle-ec5f53d'", updated)
        with self.assertRaises(ValueError):
            upgrade.updated_stop('unknown helper')

if __name__ == '__main__':
    unittest.main()
