"""Exercise the real application on its isolated Docker internal network.

Run from the repository root after building the lab image and route-mock binary.
All mutations are confined to the explicit sub2-route-lab Compose project.
"""
import datetime as dt
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]
BASE = ''
MOCK = ''
COMPOSE = ['docker', 'compose', '-p', 'sub2-route-lab', '-f', str(ROOT / 'deploy/compose.route-lab.yaml')]
ADMIN = {'email': 'admin@route-lab.invalid', 'password': 'Synthetic-Route-Lab-Password-2026!'}
RESULT = {'baseline': 'v0.2.19', 'checks': [], 'started_at': dt.datetime.now(dt.timezone.utc).isoformat()}
urllib.request.install_opener(urllib.request.build_opener(urllib.request.ProxyHandler({})))


def lab_origin(service):
    identifier = subprocess.check_output(COMPOSE + ['ps', '-q', service], text=True).strip()
    container = json.loads(subprocess.check_output(['docker', 'inspect', identifier], text=True))[0]
    assert container['Config']['Labels']['com.docker.compose.project'] == 'sub2-route-lab'
    network = json.loads(subprocess.check_output(['docker', 'network', 'inspect', 'sub2-route-lab_default'], text=True))[0]
    assert network['Internal'] is True
    return 'http://' + container['NetworkSettings']['Networks']['sub2-route-lab_default']['IPAddress'] + ':8080'


def call(path, body=None, token='', base=None, expected=200):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    request = urllib.request.Request((base or BASE) + path, data=None if body is None else json.dumps(body).encode(), headers=headers)
    try:
        response = urllib.request.urlopen(request, timeout=30)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read()
        assert response.status == expected, f'{path}: expected {expected}, got {response.status}: {raw[:300]!r}'
        result = json.loads(raw)
        return result.get('data', result)


def switch(enabled, accounts='', image='sub2api-route-test:0.2.19-r1'):
    global BASE
    env = dict(os.environ, ROUTE_EVIDENCE_ENABLED=str(enabled).lower(), ROUTE_EVIDENCE_ACCOUNT_IDS=accounts, LAB_IMAGE=image)
    subprocess.run(COMPOSE + ['up', '-d', '--no-deps', '--pull', 'never', '--wait', '--wait-timeout', '150', 'app'], env=env, check=True)
    BASE = lab_origin('app')


def login():
    return call('/api/v1/auth/login', ADMIN)['access_token']


def record(name, **values):
    RESULT['checks'].append({'name': name, **values})
    print(json.dumps(RESULT['checks'][-1]), flush=True)


def request(key, stream=False):
    # The response's server-generated client correlation ID anchors all attempts.
    req = urllib.request.Request(BASE + '/v1/responses', data=json.dumps({'model': 'gpt-4.1', 'input': 'synthetic route probe ' + str(uuid.uuid4()), 'stream': stream, 'max_output_tokens': 16}).encode(), headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json', 'X-Request-ID': str(uuid.uuid4())})
    started = time.monotonic()
    try:
        response = urllib.request.urlopen(req, timeout=30)
    except urllib.error.HTTPError as error:
        raise AssertionError(f'synthetic inference returned {error.code}: {error.read()[:400]!r}') from None
    with response:
        content = response.read().decode()
        assert response.status == 200
        assert 'synthetic route response' in content, content[:600]
        return response.headers['X-Client-Request-ID'], round((time.monotonic() - started)*1000, 2)


def events(admin, client_id):
    params = urllib.parse.urlencode({'component': 'route.outbound', 'client_request_id': client_id, 'time_range': '1h', 'page_size': 100})
    return call('/api/v1/admin/ops/system-logs?' + params, token=admin)['items']


def wait_events(admin, client_id, minimum=1):
    for _ in range(40):
        rows = events(admin, client_id)
        if len(rows) >= minimum:
            return rows
        time.sleep(.25)
    raise AssertionError('HTTP attempt events did not arrive')


def usage(admin, client_id):
    params = urllib.parse.urlencode({'request_id': 'client:' + client_id, 'page_size': 100})
    for _ in range(40):
        rows = call('/api/v1/admin/usage?' + params, token=admin)['items']
        row = next((row for row in rows if row.get('request_id') == 'client:' + client_id), None)
        if row:
            return row
        time.sleep(.25)
    raise AssertionError('usage row did not arrive')


def main():
    global BASE, MOCK
    switch(False)
    BASE, MOCK = lab_origin('app'), lab_origin('mock')
    call('/control?mode=normal', {}, base=MOCK)
    admin = login()
    user = call('/api/v1/user/profile', token=admin)
    # Mirror the upstream unit-test fixture in the isolated database. This is
    # synthetic test state, not an acknowledgement submitted for a real person.
    compliance = call('/api/v1/admin/compliance', token=admin)
    fixture = json.dumps({'version': compliance['version'], 'admin_user_id': user['id'], 'user_agent': 'synthetic-lab-fixture-not-human-consent'})
    statement = "INSERT INTO settings (key,value) VALUES ('admin_compliance_acknowledgement:%d','%s') ON CONFLICT (key) DO UPDATE SET value=excluded.value;" % (int(user['id']), fixture.replace("'", "''"))
    subprocess.run(COMPOSE + ['exec', '-T', 'postgres', 'psql', '-U', 'route_lab', '-d', 'route_lab', '-v', 'ON_ERROR_STOP=1', '-q'], input=statement, text=True, check=True)
    # Local synthetic setup is deliberately done through public admin interfaces.
    req = urllib.request.Request(BASE + '/api/v1/admin/users/' + str(user['id']), method='PUT', data=json.dumps({'concurrency': 20}).encode(), headers={'Authorization': 'Bearer ' + admin, 'Content-Type': 'application/json'})
    with urllib.request.urlopen(req) as response:
        assert response.status == 200
    call('/api/v1/admin/users/' + str(user['id']) + '/balance', {'balance': 100, 'operation': 'set', 'notes': 'synthetic isolated test credit'}, admin)
    group = call('/api/v1/admin/groups', {'name': 'route-lab-' + str(uuid.uuid4())[:8], 'platform': 'openai', 'rate_multiplier': 1, 'subscription_type': 'standard'}, admin)
    accounts = []
    for n in range(2):
        accounts.append(call('/api/v1/admin/accounts', {'name': 'synthetic-route-account-' + str(n), 'platform': 'openai', 'type': 'apikey', 'credentials': {'api_key': 'synthetic-first' if n == 0 else 'synthetic-second', 'base_url': 'http://mock:8080', 'api_protocol': 'responses'}, 'extra': {'upstream_request_id_header': 'x-request-id', 'openai_responses_mode': 'force_responses'}, 'priority': 1 if n == 0 else 20, 'concurrency': 10, 'group_ids': [group['id']]}, admin))
    key = call('/api/v1/keys', {'name': 'synthetic-route-key', 'group_id': group['id']}, admin)['key']
    client_id, latency = request(key)
    time.sleep(1.3)
    assert events(admin, client_id) == []
    record('disabled_by_default', events=0, response_ms=latency)

    switch(True)
    admin = login()
    for stream in [False, True]:
        client_id, latency = request(key, stream)
        rows = wait_events(admin, client_id)
        assert len(rows) == 1
        event = rows[0]
        extra = event['extra']
        assert extra['upstream_url'] == 'http://mock:8080/v1/responses'
        assert extra['upstream_request_id'].startswith('mock-provider-')
        assert extra['status_code'] == 200 and extra['response_received']
        row = usage(admin, client_id)
        assert row['account_id'] == event['account_id']
        assert row['upstream_request_id'] == extra['upstream_request_id']
        for secret in ['synthetic-first', 'synthetic-second', 'synthetic route probe', 'Authorization', 'request_body']:
            assert secret not in json.dumps(rows)
        record('stream' if stream else 'nonstream', events=len(rows), provider_id_matches_usage=True, account_id=event['account_id'], response_ms=latency)

    call('/control?mode=no-id', {}, base=MOCK)
    client_id, _ = request(key)
    rows = wait_events(admin, client_id)
    assert rows[0]['extra']['upstream_request_id'] == ''
    assert rows[0]['extra']['response_received'] is True
    assert not usage(admin, client_id).get('upstream_request_id')
    record('provider_omits_id', missing_id_preserved=True, actual_url_still_recorded=True)

    call('/control?mode=fail-first', {}, base=MOCK)
    client_id, _ = request(key, True)
    rows = wait_events(admin, client_id, 2)
    assert any(row['account_id'] == accounts[0]['id'] and row['extra']['status_code'] == 503 for row in rows)
    assert any(row['account_id'] == accounts[1]['id'] and row['extra']['status_code'] == 200 for row in rows)
    assert len({row['extra']['attempt_id'] for row in rows}) == len(rows)
    row = usage(admin, client_id)
    assert row['account_id'] == accounts[1]['id']
    record('automatic_retry_and_account_switch', attempts=len(rows), account_ids=sorted({row['account_id'] for row in rows}), final_account_id=row['account_id'])

    call('/control?mode=normal', {}, base=MOCK)
    switch(True, accounts='999999')
    admin = login()
    client_id, _ = request(key)
    time.sleep(1.3)
    assert events(admin, client_id) == []
    record('account_scope_excludes_other_accounts', events=0)

    for path, body in [('/system/update', {}), ('/system/rollback', {'version': '0.2.18'})]:
        result = call('/api/v1/admin'+path, body, admin, expected=409)
        assert result.get('reason') == 'CUSTOM_BUILD_MANAGED' or 'CUSTOM_BUILD_MANAGED' in json.dumps(result) or '定制' in json.dumps(result, ensure_ascii=False)
    record('official_update_and_rollback_blocked', status=409)

    usage(admin, client_id)
    history_path = '/api/v1/admin/usage?page=1&page_size=100&group_id=' + str(group['id'])
    history_ids = {row['id'] for row in call(history_path, token=admin)['items']}
    assert history_ids
    switch(False, image='sub2api-route-baseline:0.2.19')
    admin = login()
    public = call('/api/v1/settings/public')
    assert public['version'] == '0.2.19'
    client_id, _ = request(key)
    assert usage(admin, client_id)['account_id'] in {account['id'] for account in accounts}
    restored_ids = {row['id'] for row in call(history_path, token=admin)['items']}
    assert history_ids.issubset(restored_ids)
    record('rollback_to_unmodified_0_2_19', same_key_and_database_work=True, preserved_usage_rows=len(history_ids))

    switch(False)
    admin = login()
    public = call('/api/v1/settings/public')
    assert public['version'] == '0.2.19-route.1'
    request(key)
    record('restore_candidate_with_collection_disabled', same_key_and_database_work=True)
    RESULT['completed_at'] = dt.datetime.now(dt.timezone.utc).isoformat()


if __name__ == '__main__':
    try:
        main()
        RESULT['passed'] = True
    except Exception as error:
        RESULT['passed'] = False
        RESULT['failure'] = str(error)
        raise
    finally:
        (ROOT / 'artifacts/route-lab-results.json').write_text(json.dumps(RESULT, indent=2, ensure_ascii=False) + '\n')
