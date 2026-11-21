"""Dedicated transport wiring; no Docker daemon or Kubernetes API is contacted."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parent


class LiveDataTLSInstall(unittest.TestCase):
    def test_authority_only_in_renewal_job(self):
        resources = []
        for entry in yaml.safe_load((ROOT / 'k8s/kustomization.yaml').read_text())['resources']:
            resources.extend(d for d in yaml.safe_load_all((ROOT / 'k8s' / entry).read_text()) if d)
        for d in resources:
            if d['kind'] in ('Deployment', 'StatefulSet'):
                pod = d['spec']['template']['spec']
                self.assertFalse(any(v.get('secret', {}).get('secretName') == 'blerg-live-data-authority'
                                     for v in pod.get('volumes', [])), d['metadata']['name'])
        jobs = [d for d in resources if d['kind'] == 'CronJob' and d['metadata']['name'] == 'blerg-live-data-renew']
        self.assertEqual(len(jobs), 1)
        spec = jobs[0]['spec']
        self.assertEqual(spec['concurrencyPolicy'], 'Forbid')
        pod = spec['jobTemplate']['spec']['template']['spec']
        self.assertEqual(pod['serviceAccountName'], 'blerg-live-data-renew')
        self.assertEqual([v['secret']['secretName'] for v in pod['volumes'] if 'secret' in v], ['blerg-live-data-authority'])
        role = next(d for d in resources if d['kind'] == 'Role' and d['metadata']['name'] == 'blerg-live-data-renew')
        self.assertEqual(role['rules'], [{'apiGroups': [''], 'resources': ['secrets'],
            'resourceNames': ['blerg-live-data-core', 'blerg-live-data-runner'], 'verbs': ['get', 'update']}])
        binding = next(d for d in resources if d['kind'] == 'RoleBinding' and d['metadata']['name'] == 'blerg-live-data-renew')
        self.assertEqual(binding['subjects'], [{'kind': 'ServiceAccount', 'name': 'blerg-live-data-renew', 'namespace': 'blerg'}])

    def test_issuer_secret_account_isolation(self):
        resources = []
        for entry in yaml.safe_load((ROOT / 'k8s/kustomization.yaml').read_text())['resources']:
            resources.extend(d for d in yaml.safe_load_all((ROOT / 'k8s' / entry).read_text()) if d)
        accounts = {d['metadata']['name']: d for d in resources if d['kind'] == 'ServiceAccount'}
        workloads = {d['metadata']['name']: d['spec']['template']['spec'] for d in resources
                     if d['kind'] in ('Deployment', 'StatefulSet')}
        core_account = workloads['blerg-core'].get('serviceAccountName', 'default')
        self.assertEqual(core_account, 'default')
        self.assertEqual([name for name, pod in workloads.items()
                          if pod.get('serviceAccountName', 'default') == core_account], ['blerg-core'])
        issuer_role = next(d for d in resources if d['kind'] == 'Role' and
                           'blerg-live-data-issuer' in d['rules'][0].get('resourceNames', []))
        bindings = [d for d in resources if d['kind'] == 'RoleBinding' and
                    d['roleRef']['name'] == issuer_role['metadata']['name']]
        subjects = [subject for binding in bindings for subject in binding['subjects']]
        self.assertEqual(subjects, [{'kind': 'ServiceAccount', 'name': core_account, 'namespace': 'blerg'}])
        for workload, account in [('blerg-board', 'blerg-board'), ('postgres', 'blerg-postgres')]:
            with self.subTest(workload=workload):
                pod = workloads[workload]
                self.assertEqual(pod.get('serviceAccountName', 'default'), account)
                self.assertIs(pod.get('automountServiceAccountToken'), False)
                self.assertIs(accounts[account].get('automountServiceAccountToken'), False)
                self.assertNotEqual(account, core_account)
                self.assertFalse(any('serviceAccountToken' in source for volume in pod.get('volumes', [])
                                     for source in volume.get('projected', {}).get('sources', [])))

    def test_desktop_separate_bundles(self):
        compose = yaml.safe_load((ROOT / 'desktop/docker-compose.yml').read_text())
        services = compose['services']
        init = services['blerg-live-data-pki']
        self.assertEqual(init['network_mode'], 'none')
        self.assertTrue(init['read_only'])
        self.assertIn('live-data-pki', init['command'])
        main = (ROOT.parent / 'core/cmd/blerg-core/main.go').read_text()
        initializer = main.split('os.Args[1] == "live-data-pki"')[1].split('os.Args[1] == "mint"')[0]
        self.assertNotIn('log.Fatal', initializer)
        self.assertIn('failurelog.Default.Event', initializer)
        self.assertEqual(len(init['volumes']), 4)
        for role in ('core', 'runner'):
            service = services['blerg-' + role]
            self.assertIn(f'blerg-live-data-{role}:/live-data:ro', service['volumes'])
            self.assertEqual(service['environment']['BLERG_LIVE_DATA_TLS_DIR'], '/live-data')
            self.assertEqual(service['depends_on']['blerg-live-data-pki']['condition'],
                             'service_completed_successfully')
            self.assertFalse(any('8443' in port for port in service.get('ports', [])))
            self.assertFalse(any('authority' in mount for mount in service['volumes']))
        self.assertNotIn('LIVE_DATA_TLS', (ROOT / 'desktop/.env.example').read_text())

    def test_cluster_private_services_and_secrets(self):
        for role in ('core', 'runner'):
            docs = list(yaml.safe_load_all((ROOT / f'k8s/{role}-deployment.yaml').read_text()))
            pod = next(doc for doc in docs if doc['kind'] == 'Deployment')['spec']['template']['spec']
            volume = next(v for v in pod['volumes'] if v['name'] == 'live-data')
            self.assertEqual(volume['secret']['secretName'], 'blerg-live-data-' + role)
            self.assertEqual(volume['secret']['defaultMode'], 0o440)
            self.assertTrue(volume['secret']['optional'])
            self.assertNotIn('initContainers', pod)
            self.assertFalse(any(v['name'] == 'live-data-authority' for v in pod['volumes']))
            self.assertEqual(pod['securityContext']['fsGroup'], 65532)
            mount = next(m for m in pod['containers'][0]['volumeMounts'] if m['name'] == 'live-data')
            self.assertTrue(mount['readOnly'])
        docs = list(yaml.safe_load_all((ROOT / 'k8s/live-data.yaml').read_text()))
        for doc in docs:
            if doc['kind'] == 'Service':
                self.assertEqual(doc['spec']['type'], 'ClusterIP')
                self.assertEqual(doc['spec']['ports'][0]['port'], 8443)
            else:
                source = doc['spec']['ingress'][1]['from'][0]
                self.assertEqual(source['namespaceSelector']['matchLabels']['kubernetes.io/metadata.name'], 'blerg')
                self.assertIn(source['podSelector']['matchLabels']['app'], ('blerg-core', 'blerg-runner'))
        self.assertNotIn('8443', (ROOT / 'k8s/ingress.yaml').read_text())
        self.assertNotIn('live-data', (ROOT / 'k8s/cluster-runtime.yaml').read_text())
        deploy = (ROOT / 'k8s/deploy.sh').read_text()
        self.assertLess(deploy.index('provision_live_data.sh'), deploy.index('kubectl apply -k'))
        self.assertIn('render_live_data.py', deploy)
        self.assertIn('live_data_ready=false', deploy)
        self.assertIn('application deployment continues', deploy)

    def test_provisioner_refuses_failed_authority_read(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            (root / 'kubectl').write_text('#!/bin/sh\nexit 1\n')
            (root / 'docker').write_text('#!/bin/sh\necho must-not-run >&2\nexit 99\n')
            for name in ('kubectl', 'docker'):
                (root / name).chmod(0o700)
            result = subprocess.run(['bash', str(ROOT / 'k8s/provision_live_data.sh'), 'fixture-core', path],
                                    env=dict(os.environ, PATH=path + ':' + os.environ['PATH']), capture_output=True)
            self.assertEqual(result.returncode, 1)
            self.assertNotIn(b'must-not-run', result.stderr)

    def test_provisioner_uses_separate_secret_files(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            log = root / 'calls'
            (root / 'kubectl').write_text('''#!/usr/bin/env python3
import json, os, sys
with open(os.environ['CALL_LOG'], 'a') as f: f.write(json.dumps(sys.argv[1:])+'\\n')
if sys.argv[1] == 'apply': sys.stdin.read()
''')
            (root / 'docker').write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
with open(os.environ['CALL_LOG'], 'a') as f: f.write(json.dumps(sys.argv[1:])+'\\n')
root=pathlib.Path(sys.argv[sys.argv.index('-v')+1].split(':/pki')[0])
for role in ('authority','core','runner'):
    target=root/role; target.mkdir(exist_ok=True)
    for name in ('identity.json','ca.pem','key.pem'):
        (target/name).write_text('fixture')
    if role!='authority': (target/'cert.pem').write_text('public fixture')
''')
            for name in ('kubectl', 'docker'):
                (root / name).chmod(0o700)
            result = subprocess.run(['bash', str(ROOT / 'k8s/provision_live_data.sh'), 'fixture-core', path],
                                    env=dict(os.environ, PATH=path + ':' + os.environ['PATH'], CALL_LOG=str(log)),
                                    capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            calls = [json.loads(line) for line in log.read_text().splitlines()]
            creates = [c for c in calls if c[0] == 'create']
            self.assertEqual([c[3] for c in creates], ['blerg-live-data-authority', 'blerg-live-data-core', 'blerg-live-data-runner', 'blerg-live-data-issuer'])
            self.assertTrue(all(any(arg.startswith('--from-file=') for arg in c) for c in creates[:3]))
            self.assertTrue(any('--server-side' in call for call in calls))
            self.assertTrue(any(any(str(arg).endswith(':/pki:Z') for arg in call) for call in calls))
            self.assertTrue((root / 'core-live-data.sha').exists())
            self.assertTrue((root / 'runner-live-data.sha').exists())
            self.assertNotIn(b'fixture', result.stdout)

    def test_failed_provisioning_renders_listener_off(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            for role in ('core', 'runner'):
                (root / (role + '-deployment.yaml')).write_text((ROOT / ('k8s/' + role + '-deployment.yaml')).read_text())
            result = subprocess.run(['python3', str(ROOT / 'k8s/render_live_data.py'), path, path, 'false'], capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            for role in ('core', 'runner'):
                pod = next(d for d in yaml.safe_load_all((root / (role + '-deployment.yaml')).read_text()) if d['kind'] == 'Deployment')['spec']['template']
                env = {e['name']: e.get('value') for e in pod['spec']['containers'][0]['env']}
                self.assertEqual(env['BLERG_LIVE_DATA_TLS_DIR'], '')
                self.assertEqual(env['BLERG_LIVE_DATA_TLS_WARNING'], 'provisioning_unavailable')
                self.assertEqual(pod['metadata']['annotations']['blerg.ai/live-data-certificate'], 'unavailable')

    def test_issuer_epoch_is_separate_from_database(self):
        compose = yaml.safe_load((ROOT / 'desktop/docker-compose.yml').read_text())
        core = compose['services']['blerg-core']
        self.assertIn('blerg-live-data-issuer-state:/issuer-state', core['volumes'])
        self.assertFalse(any('issuer-state' in v for v in compose['services']['blerg-runner']['volumes']))
        docs = list(yaml.safe_load_all((ROOT / 'k8s/live-data-issuer-rbac.yaml').read_text()))
        role = next(d for d in docs if d['kind'] == 'Role')
        self.assertEqual(role['rules'][0]['resourceNames'], ['blerg-live-data-issuer'])
        self.assertEqual(role['rules'][0]['verbs'], ['get', 'update'])
        self.assertNotIn('live-data-issuer', (ROOT / 'k8s/cluster-runtime.yaml').read_text())
        pod = next(d for d in yaml.safe_load_all((ROOT / 'k8s/core-deployment.yaml').read_text()) if d['kind'] == 'Deployment')['spec']['template']['spec']
        env = {e['name']: e.get('value') for e in pod['containers'][0]['env']}
        self.assertEqual(env['BLERG_LIVE_DATA_AUTHORITY_BACKEND'], 'kubernetes')
        self.assertEqual(pod.get('serviceAccountName', 'default'), 'default')
        binding = next(d for d in docs if d['kind'] == 'RoleBinding')
        self.assertEqual(binding['subjects'][0]['name'], 'default')
        self.assertFalse(any(m['name'] == 'live-data-issuer-state' for m in pod['containers'][0]['volumeMounts']))


    def test_annotation_target_is_required(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            (root / 'core-deployment.yaml').write_text('unexpected template')
            result = subprocess.run(['python3', str(ROOT / 'k8s/render_live_data.py'), path, path, 'false'], capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(b'live_data_annotation_target_missing', result.stderr)


if __name__ == '__main__':
    unittest.main()
