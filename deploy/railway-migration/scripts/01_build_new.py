"""Phase B: build the new project next to the old one. Does not touch old traffic.
Order matters: the old Postgres is prepared BEFORE the config commit, because the new Postgres starts
pg_basebackup as soon as it deploys."""
import copy
import subprocess

from lib import *

inv = load('inventory.json') or sys.exit('run 00_inventory.py first')
old_cfg = load('old_env_SECRET.json')
M = load('map.json', {'svc': {}, 'vol': {}, 'temp_domains': {}})
region = CFG['region']
gw = CFG['gateway']

# 1. project
if not CFG['new'].get('project_id'):
    p = gql('new', 'mutation($i:ProjectCreateInput!){ projectCreate(input:$i){ id environments { edges { node { id name } } } } }',
            {'i': {'name': CFG['new']['project_name'], 'workspaceId': CFG['new']['workspace_id'], 'defaultEnvironmentName': CFG.get('env_name', 'production')}})['projectCreate']
    CFG['new']['project_id'] = p['id']
    CFG['new']['env_id'] = p['environments']['edges'][0]['node']['id']
    save('migration.json', CFG)
    log('created project', p['id'])
NEW_PROJ, NEW_ENV = ids('new')

# 2. empty services with the same names (no source yet -> nothing deploys)
existing = services('new')
for name, item in inv['services'].items():
    new_id = existing.get(name) or gql('new', 'mutation($i:ServiceCreateInput!){ serviceCreate(input:$i){ id } }',
                                       {'i': {'projectId': NEW_PROJ, 'name': name}})['serviceCreate']['id']
    M['svc'][name] = {'old': item['id'], 'new': new_id}
save('map.json', M)

# 3. volumes (Railway allows one volumeCreate per 30s)
for old_vid, v in inv['volumes'].items():
    if old_vid in M['vol'] or not v['service']:
        continue  # skip volumes not attached to any service
    r = gql('new', 'mutation($i:VolumeCreateInput!){ volumeCreate(input:$i){ id } }', {'i': {
        'projectId': NEW_PROJ, 'environmentId': NEW_ENV, 'serviceId': M['svc'][v['service']]['new'], 'mountPath': v['mount'], 'region': region}})
    M['vol'][old_vid] = {'new': r['volumeCreate']['id'], 'service': v['service'], 'sizeMB': v['sizeMB']}
    save('map.json', M)
    log('volume', v['service'], v['mount'], v['sizeMB'], 'MB')
    time.sleep(31)

# 4. old Postgres: allow replication + slot (reload only)
log('prepare old Postgres:\n' + ssh('old', gw['postgres'], script('prepare_old_pg.sh')))

# 5. one config commit for everything
new_cfg = env_config('new')
patch = {'services': {}, 'volumes': {}}
for name, ids_ in M['svc'].items():
    s = old_cfg['services'].get(ids_['old'], {})
    n = {}
    if s.get('source'):
        n['source'] = {k: v for k, v in s['source'].items() if k in ('repo', 'branch', 'image', 'checkSuites', 'rootDirectory')}
    if s.get('build'):
        n['build'] = copy.deepcopy(s['build'])
    dep = copy.deepcopy(s.get('deploy') or {})
    # new services get a default region (us-west2 on 10-29); a volume service in two regions fails the commit
    regions = dep.setdefault('multiRegionConfig', {})
    for r in ((new_cfg['services'].get(ids_['new']) or {}).get('deploy') or {}).get('multiRegionConfig', {}):
        regions.setdefault(r, None)
    if name in CFG['sleep_apps']:
        dep['startCommand'] = SLEEP_CMD
        dep.pop('healthcheckPath', None)
        dep.pop('healthcheckTimeout', None)
    n['deploy'] = dep
    net = s.get('networking') or {}
    nn = {k: net[k] for k in ('tcpProxies', 'privateNetworkEndpoint') if k in net}
    if nn:
        n['networking'] = nn
    vm = {M['vol'][ov]['new']: {'mountPath': x['mountPath']} for ov, x in (s.get('volumeMounts') or {}).items() if ov in M['vol']}
    if vm:
        n['volumeMounts'] = vm
    variables = {k: {'value': v['value']} for k, v in (s.get('variables') or {}).items() if 'value' in v}
    if name == gw['postgres']:
        tcp = inv['services'][name]['tcp_proxies'][0]
        variables.update({
            'MIG_SRC_HOST': {'value': tcp['domain']}, 'MIG_SRC_PORT': {'value': str(tcp['proxyPort'])},
            'MIG_SRC_PASSWORD': {'value': s['variables']['POSTGRES_PASSWORD']['value']}, 'MIG_SLOT': {'value': SLOT},
            'MIG_BOOT_B64': {'value': b64(script('pg_standby_boot.sh'))}})
        dep['startCommand'] = '/bin/bash -c "echo $MIG_BOOT_B64 | base64 -d > /tmp/boot.sh && exec bash /tmp/boot.sh"'
    n['variables'] = variables
    patch['services'][ids_['new']] = n
for ov, v in M['vol'].items():
    # size the volume before the first deploy: resizing later restarts the service even with skipDeploys
    patch['volumes'][v['new']] = {'sizeMB': v['sizeMB']}
commit_patch('new', patch, 'migration: mirror old project')
log('config committed; databases deploy now, apps build and sleep')

# 6. static egress IPs where the old service had them
for name, item in inv['services'].items():
    if item['egress']:
        ips = gql('new', 'mutation($i:EgressGatewayCreateInput!){ egressGatewayAssociationCreate(input:$i){ ipv4 } }',
                  {'i': {'environmentId': NEW_ENV, 'serviceId': M['svc'][name]['new'], 'region': region}})['egressGatewayAssociationCreate']
        new_ips = sorted(i['ipv4'] for i in ips)
        log(f'egress {name}: old={item["egress"]} new={new_ips}', 'SAME' if new_ips == item['egress'] else '!!! CHANGED - tell upstreams')

# 7. temporary service domains, renamed to the real ones at cutover
for name, item in inv['services'].items():
    if item['domains']['serviceDomains'] and name not in M['temp_domains']:
        port = item['domains']['serviceDomains'][0]['targetPort']
        d = gql('new', 'mutation($i:ServiceDomainCreateInput!){ serviceDomainCreate(input:$i){ id domain } }',
                {'i': {'environmentId': NEW_ENV, 'serviceId': M['svc'][name]['new'], 'targetPort': port}})['serviceDomainCreate']
        M['temp_domains'][name] = d['id']
        log('temp domain', name, d['domain'])
save('map.json', M)

# 8. services deployed from a local dir (no repo/image), e.g. caddy-long
le = CFG['long_entry']
env_ = dict(os.environ, RAILWAY_API_TOKEN=token('new'))
r = subprocess.run([RAILWAY_CLI, 'up', le['source_dir'], '--path-as-root', '-p', NEW_PROJ, '-e', CFG.get('env_name', 'production'),
                    '-s', le['service'], '--detach', '-m', 'migration'], capture_output=True, text=True, encoding='utf-8', errors='replace', env=env_)
log('railway up', le['service'], 'rc', r.returncode, (r.stdout + r.stderr).strip()[-200:])

for name in M['svc']:
    t = tcp_proxies('new', M['svc'][name]['new'])
    if t:
        log('new tcp proxy', name, [f"{x['domain']}:{x['proxyPort']}" for x in t])
log('next: python -X utf8 scripts/02_start_sync.py')
