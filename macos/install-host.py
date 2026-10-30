#!/usr/bin/env python3
"""Migrate an idle macOS host to standard directories, retaining legacy aliases.

Run after `make native`. Stops only this user's installed ClawFlow agents.
Existing destinations are never merged. Credentials are never printed.
"""
import argparse
import json
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import sys


def run(*args, check=True):
    return subprocess.run(args, capture_output=True, check=check)


POSTGRES_LABEL = 'com.zhoushoujianwork.clawflow.postgres'


def retire_postgres(apply):
    """Unload ClawFlow's dedicated PostgreSQL LaunchAgent after a completed cutover.

    Touches only com.zhoushoujianwork.clawflow.postgres. The plist is renamed (not
    deleted) and the PostgreSQL data directory is left alone, so `clawflow db
    rollback` and manual recovery stay possible. Other PostgreSQL services are
    never inspected or stopped.
    """
    home = Path.home()
    root = home / 'Library/Application Support/ClawFlow'
    record = root / 'data/cutover.json'
    database = root / 'data/clawflow.sqlite3'
    if not record.is_file() or not database.is_file():
        raise SystemExit('No completed cutover found; run clawflow db cutover and the acceptance checks first')
    info = json.loads(record.read_text())
    if info.get('rolled_back'):
        raise SystemExit('The cutover was rolled back; PostgreSQL is still the active store')
    plist = home / 'Library/LaunchAgents' / (POSTGRES_LABEL + '.plist')
    domain = f'gui/{os.getuid()}'
    loaded = run('launchctl', 'print', f'{domain}/{POSTGRES_LABEL}', check=False).returncode == 0
    print(f'Cutover: {info.get("cutover_at")}\nSQLite: {database}\nPostgreSQL agent loaded: {loaded}; plist present: {plist.exists()}')
    if not apply:
        print('Preview only. Use --apply to unload the dedicated PostgreSQL agent.')
        return
    if loaded:
        run('launchctl', 'bootout', f'{domain}/{POSTGRES_LABEL}')
    if plist.exists():
        plist.rename(plist.with_suffix('.plist.retired'))
    print('Retired. The PostgreSQL data directory and the pre-cutover config backup were left untouched.')


def install():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true', help='apply migration and reload services')
    parser.add_argument('--retire-postgres', action='store_true',
                        help="after a completed cutover, unload only ClawFlow's dedicated PostgreSQL LaunchAgent")
    args = parser.parse_args()
    if args.retire_postgres:
        retire_postgres(args.apply)
        return
    if sys.platform != 'darwin':
        raise SystemExit('macOS is required')
    home = Path.home()
    legacy = home / '.clawflow'
    root = home / 'Library/Application Support/ClawFlow'
    logs = home / 'Library/Logs/ClawFlow'
    source = Path(__file__).resolve().parent.parent
    domain = f'gui/{os.getuid()}'
    agents = home / 'Library/LaunchAgents'
    plists = []
    for path in agents.glob('com.zhoushoujianwork.clawflow.*.plist'):
        data = plistlib.loads(path.read_bytes())
        if data['Label'].endswith(('.controller', '.app')) or '.worker-' in data['Label']:
            plists.append((path, data))
    if legacy.is_symlink() and legacy.resolve() != root:
        raise SystemExit('Legacy path points to an unexpected destination')
    if legacy.exists() and not legacy.is_symlink() and root.exists():
        raise SystemExit('Both data directories exist; refusing to merge them')
    old_logs = legacy / 'logs'
    if old_logs.exists() and not old_logs.is_symlink() and logs.exists():
        raise SystemExit('Both log directories exist; refusing to merge them')
    if not (source / 'clawflow').is_file():
        raise SystemExit('Build the CLI first with make native')
    bundle = source / 'macos/dist/ClawFlow.app'
    if not bundle.is_dir():
        raise SystemExit('Build the app first with make native')
    print(f'Data: {root}\nLogs: {logs}\nServices: {len(plists)}')
    if not args.apply:
        print('Preview only. Use --apply to migrate an idle host.')
        return
    # Check current task authority before moving a live database or worker state.
    connection = legacy / 'local-dashboard-connection.json'
    if connection.exists():
        c = json.loads(connection.read_text())
        output = run(str(source / 'clawflow'), 'dashboard', 'snapshot', '--url', c['url'],
                     '--token-file', c['tokenFile'], '--fingerprint', c['fingerprint'])
        snapshot = json.loads(output.stdout)
        if any(t['state'] in ('pending', 'leased') for t in snapshot['board']['tasks']):
            raise SystemExit('Active tasks exist; drain the host before migration')
    else:
        for _, data in plists:
            if not data['Label'].endswith('.app') and run('launchctl', 'print', domain + '/' + data['Label'], check=False).returncode == 0:
                raise SystemExit('Cannot verify an existing host is idle without local-dashboard-connection.json')
    # Keep the app stopped until preferences and service paths agree.
    app = run('pgrep', '-f', '^/Applications/ClawFlow.app/Contents/MacOS/ClawFlow$', check=False)
    for pid in app.stdout.split():
        os.kill(int(pid), 15)
    for path, data in plists:
        status = run('launchctl', 'print', domain + '/' + data['Label'], check=False)
        if status.returncode == 0:
            run('launchctl', 'bootout', domain + '/' + data['Label'])
    run('/usr/bin/ditto', str(bundle), '/Applications/ClawFlow.app')
    root.parent.mkdir(parents=True, exist_ok=True)
    logs.parent.mkdir(parents=True, exist_ok=True)
    if legacy.exists() and not legacy.is_symlink():
        legacy.rename(root)
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    root.chmod(0o700)
    if not legacy.exists():
        legacy.symlink_to(root, target_is_directory=True)
    data_logs = root / 'logs'
    if data_logs.exists() and not data_logs.is_symlink():
        data_logs.rename(logs)
    logs.mkdir(parents=True, exist_ok=True, mode=0o700)
    logs.chmod(0o700)
    if not data_logs.exists():
        data_logs.symlink_to(logs, target_is_directory=True)
    def rewrite(text):
        return text.replace(str(legacy / 'logs'), str(logs)).replace(str(legacy), str(root))
    # Config backups and old absolute run paths remain usable through the alias.
    for file in [root / 'config/config.yaml', root / 'local-dashboard-connection.json']:
        if file.exists():
            file.write_text(rewrite(file.read_text()))
            file.chmod(0o600)
    binary = root / 'bin/clawflow'
    binary.parent.mkdir(exist_ok=True)
    candidate = binary.with_suffix('.new')
    shutil.copy2(source / 'clawflow', candidate)
    os.replace(candidate, binary)
    prefs = run('defaults', 'export', 'com.clawflow.dashboard', '-', check=False)
    if prefs.returncode == 0:
        value = plistlib.loads(prefs.stdout).get('controllerConnection')
        if isinstance(value, bytes):
            updated = rewrite(value.decode()).encode()
            run('defaults', 'write', 'com.clawflow.dashboard', 'controllerConnection', '-data', updated.hex())
    for path, data in plists:
        backup = path.with_suffix('.plist.before-standard-paths')
        if not backup.exists():
            backup.write_bytes(plistlib.dumps(data))
            backup.chmod(0o600)
        data = plistlib.loads(rewrite(plistlib.dumps(data).decode()).encode())
        path.write_bytes(plistlib.dumps(data))
        run('plutil', '-lint', str(path))
    # The controller owns an embedded SQLite file, so there is no database to order
    # before it. KeepAlive retries readiness failures at login.
    for path, data in sorted(plists, key=lambda pair: 2 if pair[1]['Label'].endswith('.app') else 1):
        run('launchctl', 'bootstrap', domain, str(path))
    print('Migration applied. Legacy aliases retained; verify controller and worker readiness.')


if __name__ == '__main__':
    install()
