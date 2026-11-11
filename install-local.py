#!/usr/bin/env python3
"""Install the local plugin while preserving unrelated YAML and making backups."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile
import yaml

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parent.parent,
                    help='CLIProxyAPI installation directory (default: parent of this repository)')
parser.add_argument('--config', type=Path, help='Active host config (default: ROOT/config.yaml)')
parser.add_argument('--source', type=Path, default=Path('/tmp/provider-balances-v0.1.0.so'),
                    help='Built shared library')
args = parser.parse_args()
root = args.root.expanduser().resolve()
config = (args.config if args.config is not None else root / 'config.yaml').expanduser().resolve()
source = args.source.expanduser().resolve()
target = root / 'plugins/linux/amd64/provider-balances-v0.1.0.so'
original = config.read_bytes()
before = yaml.safe_load(original)
assert before['plugins']['enabled'] is True
assert before['plugins']['dir'] == 'plugins'
assert 'provider-balances' not in before['plugins']['configs'], 'Plugin is already configured'
assert not target.exists(), 'Plugin is already installed'
marker = '    configs:\n'
text = original.decode()
start = text.index('plugins:\n')
index = text.index(marker, start) + len(marker)
block = ('        provider-balances:\n'
         '            enabled: true\n'
         f'            data_file: {json.dumps(str(root / "data/provider-balances.json"))}\n')
updated = (text[:index] + block + text[index:]).encode()
after = yaml.safe_load(updated)
expected = dict(after['plugins']['configs'].pop('provider-balances'))
assert expected == {'enabled': True, 'data_file': str(root / 'data/provider-balances.json')}
assert after == before, 'Unrelated configuration changed'
stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
backup = root / 'operations' / ('provider-balances-' + stamp)
backup.parent.mkdir(mode=0o700, exist_ok=True)
backup.mkdir(mode=0o700)
shutil.copy2(config, backup / 'config.before.yaml')
os.chmod(backup / 'config.before.yaml', 0o600)
target.parent.mkdir(parents=True, exist_ok=True)
shutil.copy2(source, target)
os.chmod(target, 0o644)
with tempfile.NamedTemporaryFile(dir=config.parent, prefix='.balance-install-', delete=False) as temp:
    temp.write(updated)
    temp.flush()
    os.fsync(temp.fileno())
    temp_path = Path(temp.name)
os.chmod(temp_path, config.stat().st_mode & 0o777)
os.replace(temp_path, config)
record = {'version': '0.4.1', 'config': str(config), 'plugin': str(target),
          'config_before_sha256': hashlib.sha256(original).hexdigest(),
          'config_after_sha256': hashlib.sha256(updated).hexdigest(),
          'plugin_sha256': hashlib.sha256(target.read_bytes()).hexdigest()}
(backup / 'installation.json').write_text(json.dumps(record, indent=2) + '\n')
print('Installed provider-balances 0.4.1')
print('Backup: ' + str(backup))
