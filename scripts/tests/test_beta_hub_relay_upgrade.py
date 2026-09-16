import importlib.util
from pathlib import Path
import unittest

spec=importlib.util.spec_from_file_location('hub_upgrade',Path(__file__).parents[1]/'beta_hub_relay_upgrade.py')
upgrade=importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade)

class UpgradeTests(unittest.TestCase):
    def test_only_dashboard_program_changes(self):
        old={'Label':upgrade.LABEL,'ProgramArguments':[str(upgrade.OLD),'dashboard','--config',str(upgrade.CONFIG)]}
        new,helper=upgrade.plan(old,upgrade.OLD_EXPRESSION)
        self.assertEqual(new['ProgramArguments'][0],str(upgrade.NEW))
        self.assertEqual(old['ProgramArguments'][0],str(upgrade.OLD))
        self.assertIn("role == 'dashboard'",helper)
        self.assertIn(upgrade.OLD_EXPRESSION,helper)
        self.assertNotIn('UserName',new)
    def test_other_service_or_unknown_helper_is_rejected(self):
        for label in ('com.executor.agent','com.executor.beta.agent'):
            with self.assertRaises(ValueError):
                upgrade.plan({'Label':label},upgrade.OLD_EXPRESSION)
        with self.assertRaises(ValueError):
            upgrade.plan({'Label':upgrade.LABEL,'ProgramArguments':[str(upgrade.OLD),'dashboard','--config',str(upgrade.CONFIG)]},'unknown')

if __name__=='__main__':unittest.main()
