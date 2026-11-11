#!/usr/bin/env python3
"""Back up and atomically replace the installed plugin without changing config/data."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parent.parent,
                    help='CLIProxyAPI installation directory (default: parent of this repository)')
parser.add_argument('--source', type=Path, default=Path('/tmp/provider-balances-v0.1.0.so'),
                    help='Built shared library')
args = parser.parse_args()
root = args.root.expanduser().resolve()
source = args.source.expanduser().resolve()
target = root / 'plugins/linux/amd64/provider-balances-v0.1.0.so'
data = root / 'data/provider-balances.json'
assert source.is_file() and target.is_file(), 'Build the plugin and install it before running an update'
digest = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
backup = root / 'operations' / ('provider-balances-update-' + stamp)
backup.parent.mkdir(mode=0o700, exist_ok=True)
backup.mkdir(mode=0o700)
before_sha = digest(target)
shutil.copy2(target, backup / target.name)
data_sha = digest(data) if data.exists() else None
if data.exists():
    shutil.copy2(data, backup / data.name)
    os.chmod(backup / data.name, 0o600)
staged = None
try:
    with tempfile.NamedTemporaryFile(dir=target.parent, prefix='.provider-balances-update-', delete=False) as temp:
        staged = Path(temp.name)
        with source.open('rb') as built:
            shutil.copyfileobj(built, temp)
        temp.flush()
        os.fsync(temp.fileno())
    os.chmod(staged, 0o644)
    assert digest(staged) == digest(source), 'Staged library checksum mismatch'
    os.replace(staged, target)
finally:
    if staged is not None:
        staged.unlink(missing_ok=True)
record = {'version': '0.4.1', 'plugin': str(target),
          'plugin_before_sha256': before_sha, 'plugin_after_sha256': digest(target),
          'data_backup_sha256': data_sha, 'configuration_changed': False}
(backup / 'installation.json').write_text(json.dumps(record, indent=2) + '\n')
print('Installed provider-balances 0.4.1; restart the CLIProxyAPI service to load it')
print('Backup: ' + str(backup))
