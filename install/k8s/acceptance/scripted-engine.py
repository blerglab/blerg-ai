#!/usr/bin/env python3
"""Deterministic Claude wire fixture, present only in the disposable image."""
import json
from pathlib import Path
import subprocess
import sys


def emit(value):
    print(json.dumps(value), flush=True)


def commit_members(root):
    members = sorted(p for p in Path(root).iterdir() if (p / '.git').is_dir())
    if not 2 <= len(members) <= 3:
        raise RuntimeError('expected two or three prepared members')
    for member in members:
        (member / 'acceptance-smoke.txt').write_text('acceptance smoke\n')
        subprocess.run(['git', '-C', str(member), 'add', '--', 'acceptance-smoke.txt'], check=True, stdout=subprocess.DEVNULL)
        subprocess.run(['git', '-C', str(member), '-c', 'user.name=Acceptance', '-c', 'user.email=acceptance@example.invalid',
                        'commit', '-qm', 'Acceptance smoke'], check=True, stdout=subprocess.DEVNULL)


def turn(text, root='/workspace/projects'):
    if 'commit smoke marker in every member' in text:
        commit_members(root)
    emit({'type': 'system', 'subtype': 'init', 'session_id': 'acceptance-scripted'})
    emit({'type': 'assistant', 'message': {'content': [{'type': 'text', 'text': 'Smoke fixture turn complete.'}]}})
    emit({'type': 'result', 'subtype': 'success', 'is_error': False, 'session_id': 'acceptance-scripted', 'result': 'Smoke fixture turn complete.'})


def main():
    args = sys.argv[1:]
    if '--version' in args:
        print('2.1.47 (Claude Code)')
    elif '--help' in args:
        print('--include-partial-messages --forward-subagent-text --input-format --replay-user-messages')
    elif '--input-format' in args:
        for line in sys.stdin:
            message = json.loads(line)
            if message.get('type') == 'control_request':
                emit({'type': 'control_response', 'response': {'subtype': 'success', 'request_id': message['request_id'], 'response': {'still_queued': []}}})
            elif message.get('type') == 'user':
                emit({**message, 'isReplay': True, 'session_id': 'acceptance-scripted'})
                turn(json.dumps(message.get('message', {}).get('content', '')))
    else:
        prompt = args[args.index('-p')+1] if '-p' in args and args.index('-p')+1 < len(args) and not args[args.index('-p')+1].startswith('--') else sys.stdin.read()
        turn(prompt)


if __name__ == '__main__':
    main()
