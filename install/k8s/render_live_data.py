"""Render optional TLS state without blocking the rest of an application install."""
import pathlib
import sys

render, work = map(pathlib.Path, sys.argv[1:3])
ready = sys.argv[3] == 'true'
for role in ('core', 'runner'):
    path = render / (role + '-deployment.yaml')
    digest = (work / (role + '-live-data.sha')).read_text().strip() if ready else 'unavailable'
    text = path.read_text()
    marker = '  template:\n    metadata:\n'
    if text.count(marker) != 1:
        raise SystemExit('live_data_annotation_target_missing')
    text = text.replace(marker, marker + '      annotations:\n        blerg.ai/live-data-certificate: "' + digest + '"\n', 1)
    if not ready:
        marker = '- name: BLERG_LIVE_DATA_TLS_DIR\n              value: /live-data'
        if text.count(marker) != 1:
            raise SystemExit('live_data_optional_transport_target_missing')
        text = text.replace(marker, '- name: BLERG_LIVE_DATA_TLS_DIR\n              value: ""\n            - name: BLERG_LIVE_DATA_TLS_WARNING\n              value: provisioning_unavailable', 1)
    path.write_text(text)
