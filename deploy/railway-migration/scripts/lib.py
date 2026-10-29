"""Shared helpers for the Railway account migration scripts (see ../README.md).

Config: ../work/migration.json (copy ../migration.example.json). Services are matched by NAME
between the old and new project, so the same scripts work every month.

Tokens:
  NEW_RAILWAY_TOKEN  account token of the new account (required)
  OLD_RAILWAY_TOKEN  account token of the old account (optional; falls back to the logged-in
                     Railway CLI - run `railway whoami` first if GraphQL says "Not Authorized")
"""
import base64
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
WORK = os.path.normpath(os.path.join(HERE, '..', 'work'))
os.makedirs(WORK, exist_ok=True)
API = 'https://backboard.railway.com/graphql/v2'
RAILWAY_CLI = os.environ.get('RAILWAY_CLI') or shutil.which('railway.cmd') or shutil.which('railway') or 'railway'
SLOT = 'mig_newacct'
SLEEP_CMD = 'tail -f /dev/null'
# psql inside the Postgres container (both sides have PGUSER/PGDATABASE)
PSQL = 'psql -h localhost -U "$PGUSER" -d "$PGDATABASE" -At'
T0 = time.time()


def log(*a):
    print(f'[{time.strftime("%H:%M:%S")} +{time.time() - T0:5.0f}s]', *a, flush=True)


def load(name, default=None):
    p = os.path.join(WORK, name)
    if not os.path.exists(p):
        return default
    with open(p, encoding='utf-8') as f:
        return json.load(f)


def save(name, obj):
    with open(os.path.join(WORK, name), 'w', encoding='utf-8') as f:
        json.dump(obj, f, ensure_ascii=False, indent=1)


CFG = load('migration.json')
if CFG is None:
    sys.exit(f'missing {WORK}/migration.json - copy migration.example.json and fill in the IDs')


def token(side):
    t = os.environ.get('OLD_RAILWAY_TOKEN' if side == 'old' else 'NEW_RAILWAY_TOKEN')
    if t:
        return t
    if side == 'old':
        with open(os.path.expanduser('~/.railway/config.json'), encoding='utf-8') as f:
            return json.load(f)['user']['accessToken']
    sys.exit('NEW_RAILWAY_TOKEN is not set')


def gql(side, query, variables=None, retries=3):
    body = json.dumps({'query': query, 'variables': variables or {}}).encode()
    for i in range(retries):
        req = urllib.request.Request(API, data=body, headers={
            'Content-Type': 'application/json', 'User-Agent': 'railway-migration/1.0',
            'Authorization': 'Bearer ' + token(side)})
        try:
            r = json.loads(urllib.request.urlopen(req, timeout=120).read().decode())
        except urllib.error.HTTPError as e:
            r = json.loads(e.read().decode() or '{}')
        errs = r.get('errors')
        if errs and i < retries - 1 and any('rate' in (e.get('message') or '').lower() for e in errs):
            time.sleep(5)
            continue
        if errs:
            raise RuntimeError(json.dumps(errs, ensure_ascii=False)[:1500])
        return r['data']


def ids(side):
    return CFG[side]['project_id'], CFG[side]['env_id']


def services(side):
    """name -> service id"""
    d = gql(side, 'query($p:String!){ project(id:$p){ services { edges { node { id name } } } } }', {'p': ids(side)[0]})
    return {e['node']['name']: e['node']['id'] for e in d['project']['services']['edges']}


def deployments(side, service_id, n=1):
    d = gql(side, 'query($s:String!,$e:String!){ deployments(first:%d, input:{serviceId:$s, environmentId:$e}){'
                  ' edges { node { id status createdAt instances { status } meta } } } }' % n,
            {'s': service_id, 'e': ids(side)[1]})
    return [e['node'] for e in d['deployments']['edges']]


def env_config(side, decrypt=False):
    return gql(side, 'query($e:String!,$d:Boolean){ environment(id:$e){ config(decryptVariables:$d) } }',
               {'e': ids(side)[1], 'd': decrypt})['environment']['config']


def commit_patch(side, patch, message, skip_deploys=False):
    """Commit an environment patch and wait until Railway has applied it (commits are async)."""
    env = ids(side)[1]
    gql(side, 'mutation($e:String!,$p:EnvironmentConfig!,$m:String,$s:Boolean){'
              ' environmentPatchCommit(environmentId:$e, patch:$p, commitMessage:$m, skipDeploys:$s) }',
        {'e': env, 'p': patch, 'm': message, 's': skip_deploys})
    for _ in range(40):
        time.sleep(3)
        p = gql(side, 'query($e:String!){ environmentPatches(environmentId:$e, first:1){'
                      ' edges { node { message status lastAppliedError } } } }', {'e': env})
        p = p['environmentPatches']['edges'][0]['node']
        if p['message'] == message and p['status'] in ('COMMITTED', 'FAILED'):
            break
    if p['status'] == 'FAILED':
        raise RuntimeError(f'patch "{message}" failed: {p["lastAppliedError"]}')
    return p


def ssh(side, service, script, timeout=180):
    """Run a shell script inside a service container. The script is shipped base64-encoded so
    nothing needs shell quoting."""
    env = dict(os.environ)
    env.pop('RAILWAY_API_TOKEN', None)
    if side == 'new' or os.environ.get('OLD_RAILWAY_TOKEN'):
        env['RAILWAY_API_TOKEN'] = token(side)
    b = base64.b64encode(script.encode()).decode()
    r = subprocess.run([RAILWAY_CLI, 'ssh', '-p', ids(side)[0], '-s', service, '-e', CFG.get('env_name', 'production'),
                        '--', 'sh', '-c', f'echo {b} | base64 -d > /tmp/mig.sh && sh /tmp/mig.sh'],
                       capture_output=True, text=True, encoding='utf-8', errors='replace', env=env, timeout=timeout)
    return '\n'.join(l for l in (r.stdout + r.stderr).splitlines() if 'Using SSH key' not in l).strip()


def script(name):
    with open(os.path.join(HERE, name), encoding='utf-8') as f:
        return f.read()


def b64(text):
    return base64.b64encode(text.encode()).decode()


def http(method, url, body=None, headers=None, timeout=150):
    h = {'Content-Type': 'application/json', 'User-Agent': 'railway-migration/1.0'}
    h.update(headers or {})
    req = urllib.request.Request(url, data=json.dumps(body).encode() if body is not None else None, headers=h, method=method)
    t = time.time()
    try:
        r = urllib.request.urlopen(req, timeout=timeout)
        return r.status, r.read().decode('utf-8', 'replace'), time.time() - t
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode('utf-8', 'replace'), time.time() - t
    except Exception as e:  # network errors are reported, not raised
        return 0, str(e), time.time() - t


def stop_app(side, service_id, wait=40):
    """Stop the live deployment and make sure its instances really exit. On 10-29 deploymentStop
    returned true for coincoin-proxy but the container kept running, so fall back to deploymentRemove."""
    dep = [d for d in deployments(side, service_id, 3) if d['status'] == 'SUCCESS'][0]
    gql(side, 'mutation($id:String!){ deploymentStop(id:$id) }', {'id': dep['id']})
    for mutation in ('deploymentStop', 'deploymentRemove'):
        if mutation == 'deploymentRemove':
            log('deploymentStop did not stop the instances -> deploymentRemove')
            gql(side, 'mutation($id:String!){ deploymentRemove(id:$id) }', {'id': dep['id']})
        for _ in range(wait // 5):
            time.sleep(5)
            cur = deployments(side, service_id, 1)[0]
            states = [i['status'] for i in cur['instances']]
            if all(s != 'RUNNING' for s in states):
                log('stopped', dep['id'][:8], states)
                return dep
    raise RuntimeError(f'could not stop deployment {dep["id"]}')


def wait_new_deployment(side, service_id, before_id, timeout=600):
    """Wait for a deployment other than before_id to finish. Returns it (status SUCCESS/FAILED/CRASHED)."""
    seen, end = None, time.time() + timeout
    while time.time() < end:
        d = deployments(side, service_id, 1)[0]
        if (d['id'], d['status']) != seen:
            log('deployment', d['id'][:8], d['status'])
            seen = (d['id'], d['status'])
        if d['id'] != before_id and d['status'] in ('SUCCESS', 'FAILED', 'CRASHED'):
            return d
        time.sleep(4)
    raise RuntimeError('timed out waiting for the new deployment')


def tcp_proxies(side, service_id):
    return gql(side, 'query($e:String!,$s:String!){ tcpProxies(environmentId:$e, serviceId:$s){ domain proxyPort applicationPort } }',
               {'e': ids(side)[1], 's': service_id})['tcpProxies']


def domains(side, service_id):
    return gql(side, 'query($e:String!,$s:String!,$p:String!){ domains(environmentId:$e, serviceId:$s, projectId:$p){'
                     ' serviceDomains { id domain targetPort } customDomains { id domain targetPort } } }',
               {'e': ids(side)[1], 's': service_id, 'p': ids(side)[0]})['domains']


def move_service_domain(old_sid, new_sid, new_domain_id, domain, port):
    """Release a *.up.railway.app domain on the old service and claim it on the new one (~7s on 10-29)."""
    old = [d for d in domains('old', old_sid)['serviceDomains'] if d['domain'] == domain]
    if old:
        gql('old', 'mutation($id:String!){ serviceDomainDelete(id:$id) }', {'id': old[0]['id']})
    for _ in range(30):
        if gql('new', 'query($d:String!){ serviceDomainAvailable(domain:$d){ available } }', {'d': domain})['serviceDomainAvailable']['available']:
            break
        time.sleep(2)
    gql('new', 'mutation($i:ServiceDomainUpdateInput!){ serviceDomainUpdate(input:$i) }', {'i': {
        'environmentId': ids('new')[1], 'serviceId': new_sid, 'serviceDomainId': new_domain_id, 'domain': domain, 'targetPort': port}})
    log('service domain', domain, 'now on the new service')


def cf(method, path, cf_token, body=None):
    req = urllib.request.Request('https://api.cloudflare.com/client/v4' + path,
                                 data=json.dumps(body).encode() if body else None, method=method,
                                 headers={'Authorization': 'Bearer ' + cf_token, 'Content-Type': 'application/json'})
    return json.loads(urllib.request.urlopen(req, timeout=30).read())


def cf_zone_id(zone, cf_token):
    return cf('GET', f'/zones?name={zone}', cf_token)['result'][0]['id']


def pg_params(side, service):
    q = ("SELECT name||'='||setting||coalesce(unit,'') FROM pg_settings WHERE source NOT IN "
         "('default','override','client','session') AND name NOT IN ('primary_conninfo','primary_slot_name') ORDER BY 1")
    return sorted(set(ssh(side, service, f'{PSQL} -c "{q}"').splitlines()))


MYSQL_GLOBALS = ('innodb_buffer_pool_size', 'max_connections', 'innodb_flush_log_at_trx_commit', 'wait_timeout',
                 'max_allowed_packet', 'sql_mode', 'time_zone', 'character_set_server', 'collation_server',
                 'table_open_cache', 'innodb_lock_wait_timeout', 'transaction_isolation', 'log_bin', 'performance_schema')


def mysql_params(side, service):
    """{'persisted': {name: value}, 'globals': {name: value}}"""
    names = ','.join(f"'{n}'" for n in MYSQL_GLOBALS)
    out = ssh(side, service, 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"\n'
              'mysql -uroot -N -e "SELECT CONCAT(\'P|\',VARIABLE_NAME,\'|\',VARIABLE_VALUE) FROM performance_schema.persisted_variables"\n'
              f'mysql -uroot -N -e "SELECT CONCAT(\'G|\',VARIABLE_NAME,\'|\',VARIABLE_VALUE) FROM performance_schema.global_variables WHERE VARIABLE_NAME IN ({names})"\n')
    res = {'persisted': {}, 'globals': {}}
    for line in out.splitlines():
        parts = line.split('|', 2)
        if len(parts) == 3 and parts[0] in ('P', 'G'):
            res['persisted' if parts[0] == 'P' else 'globals'][parts[1]] = parts[2]
    return res


REDIS_KEYS = ('maxmemory', 'maxmemory-policy', 'appendonly', 'appendfsync', 'save', 'maxclients', 'timeout',
              'tcp-keepalive', 'io-threads', 'databases', 'notify-keyspace-events', 'hz')


def redis_params(side, service):
    sh = 'R="redis-cli -a $REDIS_PASSWORD --no-auth-warning"\n' + ''.join(
        f'echo "{k}=$($R CONFIG GET {k} | tail -1)"\n' for k in REDIS_KEYS)
    return dict(l.split('=', 1) for l in ssh(side, service, sh).splitlines() if '=' in l)


def image_of(service_cfg):
    return ((service_cfg or {}).get('source') or {}).get('image') or ''
