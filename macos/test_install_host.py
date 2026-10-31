"""Host service tests: never invoke the machine's real launchctl."""
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location('install_host', Path(__file__).with_name('install-host.py'))
host = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host)


class RetirePostgresTests(unittest.TestCase):
    def test_alternate_home_cannot_address_real_user_services(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            data = home / 'Library/Application Support/ClawFlow/data'
            data.mkdir(parents=True)
            (data / 'cutover.json').write_text(json.dumps({'cutover_at': 'fixture'}))
            (data / 'clawflow.sqlite3').touch()
            agents = home / 'Library/LaunchAgents'
            agents.mkdir(parents=True)
            plist = agents / (host.POSTGRES_LABEL + '.plist')
            plist.write_text('<plist/>')
            for apply in (False, True):
                with self.subTest(apply=apply), patch.object(host.Path, 'home', return_value=home), patch.object(host, 'run') as run:
                    with self.assertRaisesRegex(SystemExit, 'HOME'):
                        host.retire_postgres(apply)
                    run.assert_not_called()
                    self.assertTrue(plist.exists())

    def test_account_home_keeps_preview_and_retirement_behavior(self):
        for apply in (False, True):
            with self.subTest(apply=apply), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                data = home / 'Library/Application Support/ClawFlow/data'
                data.mkdir(parents=True)
                (data / 'cutover.json').write_text(json.dumps({'cutover_at': 'fixture'}))
                database = data / 'clawflow.sqlite3'
                database.touch()
                agents = home / 'Library/LaunchAgents'
                agents.mkdir(parents=True)
                plist = agents / (host.POSTGRES_LABEL + '.plist')
                plist.touch()
                with patch.object(host.Path, 'home', return_value=home), patch.object(host.pwd, 'getpwuid', return_value=SimpleNamespace(pw_dir=str(home))), patch.object(host, 'run', return_value=SimpleNamespace(returncode=0)) as run:
                    host.retire_postgres(apply)
                commands = [call.args for call in run.call_args_list]
                self.assertEqual(commands, [('launchctl', 'print', f'gui/{host.os.getuid()}/{host.POSTGRES_LABEL}')] + ([('launchctl', 'bootout', f'gui/{host.os.getuid()}/{host.POSTGRES_LABEL}')] if apply else []))
                self.assertEqual(plist.exists(), not apply)
                self.assertEqual(plist.with_suffix('.plist.retired').exists(), apply)
                self.assertTrue(database.exists())


if __name__ == '__main__':
    unittest.main()
