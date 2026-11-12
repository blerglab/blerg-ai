"""Conservative touched-test selection; the complete gate remains authoritative."""
import json
import os
from pathlib import Path
import subprocess

MODULES = ('contracts', 'core', 'board', 'runner')
APPS = {'core': 'core/web', 'board': 'board/web', 'chat': 'packages/chat', 'runner': 'runner/frontend'}


def changed_files(root, base):
    # Include both sides of renames/deletions and all uncommitted/untracked input.
    base = subprocess.check_output(['git', 'rev-parse', '--verify', '--end-of-options', base + '^{commit}'],
                                   cwd=root, text=True).strip()
    tracked = subprocess.check_output(['git', 'diff', '--name-only', '--no-renames', '-z', base, '--'], cwd=root)
    other = subprocess.check_output(['git', 'ls-files', '--others', '--exclude-standard', '-z'], cwd=root)
    return sorted(set(os.fsdecode(p) for p in (tracked + other).split(b'\0') if p))


def json_objects(raw):
    decoder = json.JSONDecoder()
    while raw.strip():
        obj, end = decoder.raw_decode(raw.lstrip())
        yield obj
        raw = raw.lstrip()[end:]


def go_packages(root, go, workers):
    packages = []
    env = dict(os.environ, GOWORK='off', GOMAXPROCS=str(workers), GOFLAGS=f'-p={workers}')
    for module in MODULES:
        raw = subprocess.check_output([go, 'list', '-json', './...'], cwd=root / module, env=env, text=True)
        for package in json_objects(raw):
            package['module'] = module
            package['relative_dir'] = Path(package['Dir']).relative_to(root).as_posix()
            packages.append(package)
    return packages


def strip_trailing_packages(command):
    """Drop the trailing package arguments (one or more), keeping the command and its
    flags before them. Every package in this matrix is a relative path starting with
    './'; runner-go now excludes race-only packages, so it can end with several of
    them instead of a single './...'."""
    index = len(command)
    while index > 0 and command[index - 1].startswith('./'):
        index -= 1
    return command[:index]


def affected_packages(files, packages):
    affected = set()
    for file in files:
        module = file.split('/')[0]
        candidates = [p for p in packages if p['module'] == module]
        if not candidates:
            continue
        owners = [p for p in candidates if file.startswith(p['relative_dir'] + '/')]
        # The nearest package owns fixtures, embedded SQL and testdata too.
        if owners and file not in (module + '/go.mod', module + '/go.sum'):
            affected.add(max(owners, key=lambda p: len(p['relative_dir']))['ImportPath'])
        else:
            affected.update(p['ImportPath'] for p in candidates)
    while True:
        importers = {p['ImportPath'] for p in packages
                     if affected.intersection(p.get('Imports', []) + p.get('TestImports', []) + p.get('XTestImports', []))}
        expanded = affected | importers
        if expanded == affected:
            return {module: sorted('./' + p['relative_dir'][len(module)+1:] if p['relative_dir'] != module else '.'
                                   for p in packages if p['module'] == module and p['ImportPath'] in affected)
                    for module in MODULES}
        affected = expanded


def select(suites, root, files, packages=None, *, go='go', workers=2):
    """Reduce only the touched tier. Unknown inputs conservatively select all."""
    go_files, frontend_files, apps = [], [], set()
    all_go = all_web = scripts = cli = docs = False
    full_apps = set()
    for file in files:
        path = Path(file)
        app = next((name for name, directory in APPS.items() if file.startswith(directory + '/')), None)
        if app:
            apps.add(app)
            if app == 'chat':
                apps.update(('board', 'runner'))
            frontend_files.append(str(root / file))
            # Deleted imports/configuration cannot be resolved by Vitest related.
            if not (root / file).exists() or not file.startswith(APPS[app] + '/src/') or '/src/test/' in file or path.name.endswith('.d.ts') or path.suffix not in ('.ts', '.tsx', '.js', '.jsx'):
                full_apps.update(('chat', 'board', 'runner') if app == 'chat' else (app,))
        elif file.startswith('packages/'):
            all_web = True
        elif file.startswith('runner/.claude/skills/session-messaging/'):
            cli = True
        elif file.startswith('scripts/') or file.startswith('install/') or file.startswith('video/'):
            # Test tooling can affect every language's checks.
            scripts = all_go = all_web = cli = True
        elif file.endswith('.md') or file.startswith('docs/'):
            docs = True
        elif file.split('/')[0] in MODULES:
            go_files.append(file)
            if path.suffix in ('.sh', '.ps1', '.py'):
                scripts = True
        else:
            all_go = all_web = scripts = cli = True
    if go_files and not all_go and packages is None:
        packages = go_packages(root, go, workers)
    selected_go = {module: ['./...'] for module in MODULES} if all_go else affected_packages(go_files, packages or [])
    if all_web:
        apps.update(APPS)
        full_apps.update(APPS)
    result = []
    for name, cwd, command in suites:
        command = list(command)
        module = next((m for m in MODULES if name in (f'{m}-go', f'{m}-vet', f'{m}-go-lint')), None)
        if module and not selected_go[module]:
            continue
        if module:
            if name.endswith('-go-lint'):
                # Full affected-module lint retains cross-package analyzer accuracy.
                result.append((name, cwd, command))
            else:
                result.append((name, cwd, strip_trailing_packages(command) + selected_go[module]))
        elif name in ('gofmt', 'lint-pin') and any(selected_go.values()):
            result.append((name, cwd, command))
        elif name in ('cli-unittest', 'cli-pytest') and cli:
            result.append((name, cwd, command))
        elif name in ('scripts', 'verification-go-lint') and scripts:
            result.append((name, cwd, command))
        elif name == 'host-tracing-preflight':
            # Normal runner-go includes real helper/security tests. Focused callers
            # can select nontracing suites explicitly on restricted hosts.
            if any(p in ('./...', './internal/daemon', './internal/shellruntime', './internal/processcontainment') for p in selected_go['runner']):
                result.append((name, cwd, command))
        else:
            app = name.split('-')[0]
            if app not in apps:
                continue
            if name in (f'{app}-tests', 'runner-unit', 'runner-browser') and app not in full_apps:
                # `related` includes a changed test itself and transitive imports.
                # Empty matches are valid in this focused tier, never in the full gate.
                command = ['npx', '--no-install', 'vitest', 'related', '--run', '--passWithNoTests',
                           *frontend_files, *command[command.index('--') + 1:]]
            result.append((name, cwd, command))
    if docs and not scripts:
        result.append(('docs', '.', ['bash', './scripts/check_links.sh']))
    return result
