import importlib.machinery
import importlib.util
import json
from pathlib import Path

import pytest


def cli():
    loader = importlib.machinery.SourceFileLoader('snapshot_cli', str(Path(__file__).with_name('blerg-runner')))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


def test_snapshot_filters_and_fifty_cards(monkeypatch, capsys):
    module = cli()
    calls = []
    board = {'id': 'board-a', 'name': 'Release', 'columns': [{'id': 'todo', 'name': 'To do'}, {'id': 'done', 'name': 'Done', 'is_terminal': True}], 'tickets': [{'number': i, 'title': f'Card {i}', 'column_id': 'todo', 'tags': ['0.6.0'], 'priority': 'high', 'body': 'omit private detail'} for i in range(1, 51)]}
    monkeypatch.setattr(module, '_board_get', lambda path: calls.append(path) or board)
    module.cmd_board_snapshot('board-a', tags=['0.6.0'], mode='cards')
    data = json.loads(capsys.readouterr().out.split('\n')[1])
    assert calls == ['/api/boards/board-a']
    assert data['filters'] == {'tag': ['0.6.0']}
    assert len(data['columns'][0]['cards']) == 50
    assert 'body' not in data['columns'][0]['cards'][0]
    assert data['columns'][1]['cards'] == []
    assert data['as_of'].endswith('Z')


def test_snapshot_respects_access_refusal(monkeypatch, capsys):
    module = cli()
    def refused(path):
        raise SystemExit(1)
    monkeypatch.setattr(module, '_board_get', refused)
    with pytest.raises(SystemExit):
        module.cmd_board_snapshot('board-a')
    assert capsys.readouterr().out == ''


def test_snapshot_command_dispatch_and_credentials():
    import blerg_runner_board_test as existing
    board = {**existing.CLEAR_BOARD, "tickets": [{**existing.CLEAR_BOARD["tickets"][0], "number": 7}]}
    server = existing.make_stub(board_response=board)
    import threading
    thread = threading.Thread(target=existing._serve_n, args=(server, 1))
    thread.start()
    try:
        result = existing.run_cli(['board', 'snapshot', existing.TEST_BOARD_ID], existing._board_env(server.server_address[1]))
        assert result.returncode == 0, result.stderr
        assert result.stdout.startswith('```board-snapshot\n')
        assert server.recorded[0]['auth'] == 'Bearer ' + existing.TEST_BOARD_TOKEN
    finally:
        thread.join(timeout=5)
        server.server_close()


from blerg_runner_board_test import session_board  # noqa: F401


@pytest.mark.parametrize('level', ['propose', 'read', 'file', 'full'])
@pytest.mark.parametrize('command', [['board', 'snapshot'], ['ticket', 'render', '--tag', '0.6.0']])
def test_snapshot_session_access(session_board, level, command):
    import blerg_runner_board_test as existing
    server, env = session_board
    server.boards[0]['level'] = level
    server.cards = [{**existing.CLEAR_BOARD['tickets'][0], 'number': 7, 'tags': ['0.6.0']}]
    result = existing.run_cli(command, env)
    if level == 'propose':
        assert '```board-snapshot' not in result.stdout
        assert 'access' in result.stderr.lower()
    else:
        assert result.returncode == 0, result.stderr
        data = json.loads(result.stdout.split('\n')[1])
        assert data['columns'][0]['cards'][0]['number'] == 7
    assert all(call[2] == 'Bearer ' + existing.SESSION_TOKEN for call in server.calls)
