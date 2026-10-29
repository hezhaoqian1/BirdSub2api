"""Phase E: verification. Read-only except --models, which creates one temporary API key per active
gateway group, sends one tiny request with that group's main model/endpoint, and always deletes the keys.
usage: 20_verify.py [--models]"""
import concurrent.futures
import secrets

from lib import *

M = load('map.json')
inv = load('inventory.json')
gw = CFG['gateway']
problems = []


def check(ok, msg):
    print(('   ok  ' if ok else '   !!  ') + msg)
    if not ok:
        problems.append(msg)


print('== entry points')
for u in CFG['check_urls']:
    st, _, t = http('GET', u, timeout=20)
    check(st == 200, f'{u} -> {st} ({t:.1f}s)')
le = CFG['long_entry']
tcp = tcp_proxies('new', M['svc'][le['service']]['new'])[0]
u = f"https://{le['dns_record']}:{tcp['proxyPort']}/v1/models"
st, _, _ = http('GET', u, timeout=20)
check(st == 401, f'{u} -> {st} (expect 401 without key)')

print('== instances (new must be RUNNING; old apps must not be)')
for name, i in M['svc'].items():
    n = [x['status'] for x in deployments('new', i['new'], 1)[0]['instances']]
    o = [x['status'] for x in (deployments('old', i['old'], 1) or [{'instances': []}])[0]['instances']]
    check(n and all(s == 'RUNNING' for s in n), f'{name:<14} new={n} old={o}')
    if name in CFG['sleep_apps']:
        check(all(s != 'RUNNING' for s in o), f'{name:<14} old app stopped')

print('== config: old vs new')
oc, nc = env_config('old'), env_config('new')
for name, i in M['svc'].items():
    a, b = oc['services'].get(i['old'], {}), nc['services'].get(i['new'], {})
    for sec in ('deploy', 'build'):
        x, y = a.get(sec) or {}, b.get(sec) or {}
        for k in sorted(set(x) | set(y)):
            if k == 'multiRegionConfig':
                xr = {r: v for r, v in (x.get(k) or {}).items() if v}
                yr = {r: v for r, v in (y.get(k) or {}).items() if v}
                check(xr == yr, f'{name}: replicas old={xr} new={yr}')
            elif x.get(k) != y.get(k) and k not in ('requiredMountPath',):
                check(False, f'{name}: {sec}.{k} old={str(x.get(k))[:60]} new={str(y.get(k))[:60]}')
    va = {k for k in (a.get('variables') or {}) if not k.startswith('MIG_')}
    vb = {k for k in (b.get('variables') or {}) if not k.startswith('MIG_')}
    check(va == vb, f'{name}: variable names (only old={sorted(va - vb)}, only new={sorted(vb - va)})')
    la = gql('old', 'query($e:String!,$s:String!){ serviceInstanceLimits(environmentId:$e, serviceId:$s) }', {'e': ids('old')[1], 's': i['old']})
    lb = gql('new', 'query($e:String!,$s:String!){ serviceInstanceLimits(environmentId:$e, serviceId:$s) }', {'e': ids('new')[1], 's': i['new']})
    if la != lb:
        check(False, f'{name}: resource limits differ {la} vs {lb}')
    eg = sorted(e['ipv4'] for e in gql('new', 'query($e:String!,$s:String!){ egressGateways(environmentId:$e, serviceId:$s){ ipv4 } }',
                                        {'e': ids('new')[1], 's': i['new']})['egressGateways'])
    if inv['services'][name]['egress'] or eg:
        check(eg == inv['services'][name]['egress'], f'{name}: egress old={inv["services"][name]["egress"]} new={eg}')
for ov, v in M['vol'].items():
    check(nc['volumes'].get(v['new'], {}).get('sizeMB') == v['sizeMB'], f"volume {v['service']}: new size {nc['volumes'].get(v['new'], {}).get('sizeMB')} MB, old {v['sizeMB']} MB")

print('== database parameters')
for name, item in inv['services'].items():
    try:
        if 'pg_params' in item:
            a, b = set(item['pg_params']), set(pg_params('new', name))
            check(a == b, f'{name}: postgres settings only-old={sorted(a - b)} only-new={sorted(b - a)}')
        if 'mysql_params' in item:
            n = mysql_params('new', name)
            for k, v in item['mysql_params']['persisted'].items():
                if k != 'server_id':
                    check(n['globals'].get(k, n['persisted'].get(k)) == v, f'{name}: {k} old={v} new={n["persisted"].get(k, n["globals"].get(k))}')
            for k in ('innodb_buffer_pool_size', 'innodb_flush_log_at_trx_commit', 'sql_mode', 'time_zone', 'max_allowed_packet'):
                check(item['mysql_params']['globals'].get(k) == n['globals'].get(k), f'{name}: {k} old={item["mysql_params"]["globals"].get(k)} new={n["globals"].get(k)}')
        if 'redis_params' in item:
            n = redis_params('new', name)
            check(item['redis_params'] == n, f'{name}: redis config {"same" if item["redis_params"] == n else {k: (item["redis_params"].get(k), n.get(k)) for k in n if item["redis_params"].get(k) != n.get(k)}}')
    except Exception as ex:
        check(False, f'{name}: could not read parameters ({str(ex)[:100]})')

print('== gateway traffic (last 15 min)')
print(ssh('new', gw['postgres'], f"{PSQL} -c \"SELECT 'requests', count(*) FROM usage_logs WHERE created_at > now()-interval '15 minutes'\""
                                 f" -c \"SELECT 'errors_' || status_code, count(*) FROM ops_error_logs WHERE created_at > now()-interval '15 minutes' AND status_code>=500 GROUP BY status_code\""))


def model_tests():
    print('== gateway model tests (one temporary key per active group)')
    v = env_config('new', decrypt=True)['services'][M['svc'][gw['service']]['new']]['variables']
    base = f"https://{gw['domain']}"
    st, body, _ = http('POST', base + '/api/v1/auth/login', {'email': os.environ.get('ADMIN_EMAIL') or v['ADMIN_EMAIL']['value'],
                                                            'password': os.environ.get('ADMIN_PASSWORD') or v['ADMIN_PASSWORD']['value']})
    data = json.loads(body).get('data') or {}
    auth = {'Authorization': 'Bearer ' + (data.get('access_token') or data.get('token') or sys.exit(f'admin login failed: {st} {body[:200]}'))}
    q = ("SELECT DISTINCT ON (group_id) group_id, model, inbound_endpoint FROM (SELECT group_id, model, inbound_endpoint, count(*) n "
         "FROM usage_logs WHERE created_at > now()-interval '24 hours' AND group_id IS NOT NULL GROUP BY 1,2,3 HAVING count(*) >= 20) x "
         "ORDER BY group_id, n DESC")
    cases = [l.split('|') for l in ssh('new', gw['postgres'], f'{PSQL} -c "{q}"').splitlines() if l.count('|') == 2]
    keys = {}
    try:
        for gid, _, _ in cases:
            st, body, _ = http('POST', base + '/api/v1/keys', {'name': f'mig-verify-g{gid}', 'group_id': int(gid),
                                                              'custom_key': f'migverify-{gid}-' + secrets.token_hex(12), 'quota': 5, 'expires_in_days': 1}, auth)
            d = json.loads(body).get('data') if body.startswith('{') else None
            if st == 200 and d and d.get('key'):
                keys[gid] = (d['id'], d['key'])
            else:
                check(False, f'g{gid}: could not create test key ({st} {body[:100]})')
        prompt = 'Reply with exactly the word: OK'

        def run(case):
            gid, model, ep = case
            if gid not in keys:
                return case, '-', 0, 'no key'
            k = keys[gid][1]
            if ep == '/v1/messages':
                st, b, t = http('POST', base + ep, {'model': model, 'max_tokens': 32, 'messages': [{'role': 'user', 'content': prompt}]},
                                {'x-api-key': k, 'anthropic-version': '2023-06-01'})
            elif ep == '/v1/chat/completions':
                st, b, t = http('POST', base + ep, {'model': model, 'max_tokens': 64, 'messages': [{'role': 'user', 'content': prompt}]},
                                {'Authorization': 'Bearer ' + k})
            elif ep == '/v1/responses':
                st, b, t = http('POST', base + ep, {'model': model, 'stream': True, 'store': False, 'instructions': 'You are a helpful assistant.',
                                                    'input': [{'role': 'user', 'content': [{'type': 'input_text', 'text': prompt}]}]},
                                {'Authorization': 'Bearer ' + k})
            elif ep.startswith('/v1beta'):
                st, b, t = http('POST', f'{base}/v1beta/models/{model}:generateContent',
                                {'contents': [{'role': 'user', 'parts': [{'text': prompt}]}], 'generationConfig': {'maxOutputTokens': 512}},
                                {'x-goog-api-key': k})
            else:
                return case, '-', 0, f'endpoint {ep} not covered'
            return case, st, t, b.replace('\n', ' ')[:90] if st != 200 else 'ok'

        with concurrent.futures.ThreadPoolExecutor(6) as ex:
            for (gid, model, ep), st, t, note in ex.map(run, cases):
                check(st == 200, f'g{gid:<4} {model:<24} {ep:<22} HTTP {st} {t:.1f}s {note}')
    finally:
        for gid, (kid, _) in keys.items():
            st, _, _ = http('DELETE', f'{base}/api/v1/keys/{kid}', headers=auth)
            if st != 200:
                print(f'   !!  could not delete test key {kid} (g{gid}) - delete it by hand')
        print(f'   deleted {len(keys)} temporary keys')


if '--models' in sys.argv:
    model_tests()
print(f'\n{len(problems)} problem(s)' + (':\n - ' + '\n - '.join(problems) if problems else ''))
