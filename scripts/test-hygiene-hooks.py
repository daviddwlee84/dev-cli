#!/usr/bin/env python3
"""Exercise real commit blocking in a private temporary HOME/repository."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dev', required=True, type=Path)
    args = parser.parse_args()
    binary = args.dev.resolve()
    with tempfile.TemporaryDirectory(prefix='dev-hygiene-hooks-') as tmp:
        base = Path(tmp).resolve()
        home, repo = base / 'home', base / 'repo'
        home.mkdir(); repo.mkdir()
        env = dict(os.environ)
        env.update(HOME=str(home), USERPROFILE=str(home), XDG_DATA_HOME=str(home / 'data'),
                   XDG_CONFIG_HOME=str(home / 'config'), XDG_CACHE_HOME=str(home / 'cache'),
                   GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull,
                   GIT_CONFIG_COUNT='0', PRE_COMMIT_HOME=str(home / 'pre-commit'))
        for key in ['HERDR_ENV', 'HERDR_WORKSPACE_ID', 'HERDR_PANE_ID', 'TMUX', 'TMUX_PANE']:
            env.pop(key, None)
        env['PATH'] = str(binary.parent) + os.pathsep + env['PATH']

        def run(*command, success=True):
            result = subprocess.run(command, cwd=repo, env=env, capture_output=True, text=True)
            if success and result.returncode:
                raise RuntimeError('command failed: ' + command[0] + ' (private output withheld)')
            return result

        run('git', 'init', '-q', '-b', 'main')
        run('git', 'config', 'user.name', 'Hygiene fixture')
        run('git', 'config', 'user.email', 'hygiene@example.invalid')
        run('git', 'config', 'core.autocrlf', 'false')
        (repo / 'README.md').write_text('Synthetic hook fixture.\n')
        run('git', 'add', 'README.md')
        run('git', 'commit', '-qm', 'initial')
        dev = [str(binary), '--no-runtime', 'hygiene', '--repo', str(repo)]
        plan = json.loads(run(*dev, '--json', 'setup').stdout)
        run(*dev, 'setup', '--apply', '--plan', plan['id'], '--yes')
        run('git', 'add', '.pre-commit-config.yaml', '.gitleaks.toml', '.dev-cli/hygiene.toml')
        run('git', 'commit', '-qm', 'configure hygiene')
        target = repo / '.hidden' / 'fixture.txt'; target.parent.mkdir()
        canary = 'sk-proj-' + ('aB3dE5gH7jK9mN2pQ4sT6vW8xY0z' * 4)
        target.write_text('[REDACTED:example] ' + canary + '\n')
        run('git', 'add', '.hidden/fixture.txt')
        run('git', 'update-index', '--chmod=+x', '.hidden/fixture.txt')
        index_before = run('git', 'ls-files', '--stage', '-z').stdout
        config_before = (repo / '.git/config').read_bytes()
        failed = run('git', 'commit', '-qm', 'must be blocked', success=False)
        if run('git', 'ls-files', '--stage', '-z').stdout != index_before:
            raise RuntimeError('hook changed staged content or executable modes')
        if (repo / '.git/config').read_bytes() != config_before:
            raise RuntimeError('hook changed caller Git config')
        if failed.returncode == 0 or canary in failed.stdout + failed.stderr:
            raise RuntimeError('commit gate did not block safely')
        target.write_text('Safe fixture.\n')
        run('git', 'add', '.hidden/fixture.txt')
        run('git', 'commit', '-qm', 'safe fixture')
        print('Real hygiene hook: setup, hidden same-line secret blocking, masked output and clean commit passed.')


if __name__ == '__main__':
    main()
