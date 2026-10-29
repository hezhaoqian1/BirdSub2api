"""Phase A: read-only inventory of the OLD project.
Writes work/old_env_SECRET.json (decrypted env config) and work/inventory.json."""
from lib import *

pid, env = ids('old')

# every project in the old account, so nothing outside fearless-luck is forgotten
ws = gql('old', '{ me { email workspaces { name projects { edges { node { id name } } } } } }')['me']
log('old account', ws['email'])
for w in ws['workspaces']:
    for p in w['projects']['edges']:
        mark = '  <- this migration' if p['node']['id'] == pid else ''
        print(f"   project {p['node']['name']:<22} {p['node']['id']}{mark}")

cfg = env_config('old', decrypt=True)
save('old_env_SECRET.json', cfg)
svcs = services('old')
name_of = {v: k for k, v in svcs.items()}

vols = gql('old', 'query($p:String!){ project(id:$p){ volumes { edges { node { id name volumeInstances { edges { node {'
                  ' serviceId mountPath sizeMB currentSizeMB } } } } } } } }', {'p': pid})['project']['volumes']['edges']
inv = {'services': {}, 'volumes': {}}
for v in vols:
    for vi in v['node']['volumeInstances']['edges']:
        vi = vi['node']
        inv['volumes'][v['node']['id']] = {'name': v['node']['name'], 'service': name_of.get(vi['serviceId']),
                                           'mount': vi['mountPath'], 'sizeMB': vi['sizeMB'], 'usedMB': round(vi['currentSizeMB'] or 0)}

for name, sid in sorted(svcs.items()):
    s = cfg['services'].get(sid, {})
    dep = (deployments('old', sid, 1) or [{}])[0]
    egress = gql('old', 'query($e:String!,$s:String!){ egressGateways(environmentId:$e, serviceId:$s){ ipv4 } }', {'e': env, 's': sid})['egressGateways']
    limits = gql('old', 'query($e:String!,$s:String!){ serviceInstanceLimits(environmentId:$e, serviceId:$s) }', {'e': env, 's': sid})['serviceInstanceLimits']
    item = {
        'id': sid, 'image': image_of(s), 'repo': (s.get('source') or {}).get('repo'),
        'replicas': (s.get('deploy') or {}).get('multiRegionConfig'),
        'start_command': (s.get('deploy') or {}).get('startCommand'),
        'tcp_proxies': tcp_proxies('old', sid), 'egress': sorted(e['ipv4'] for e in egress),
        'domains': domains('old', sid), 'limits': limits,
        'deployment': {'status': dep.get('status'), 'commit': ((dep.get('meta') or {}).get('commitHash') or '')[:12],
                       'instances': [i['status'] for i in dep.get('instances') or []]},
    }
    img = item['image']
    try:
        if 'postgres' in img:
            item['pg_params'] = pg_params('old', name)
        elif img.startswith('mysql'):
            item['mysql_params'] = mysql_params('old', name)
        elif img.startswith('redis'):
            item['redis_params'] = redis_params('old', name)
    except Exception as ex:  # a DB we cannot ssh into should not stop the inventory
        item['params_error'] = str(ex)[:200]
    inv['services'][name] = item
    doms = [d['domain'] for d in item['domains']['serviceDomains']] + [d['domain'] for d in item['domains']['customDomains']]
    tcp = [f"{t['domain']}:{t['proxyPort']}->{t['applicationPort']}" for t in item['tcp_proxies']]
    print(f"   {name:<14} {item['deployment']['status']!s:<8} inst={item['deployment']['instances']} replicas={item['replicas']}"
          f"\n{'':18}src={item['repo'] or img} commit={item['deployment']['commit']} domains={doms} tcp={tcp} egress={item['egress']}")
    if item.get('mysql_params'):
        print(f"{'':18}mysql persisted={item['mysql_params']['persisted']}")

for vid, v in inv['volumes'].items():
    print(f"   volume {v['name']:<22} -> {v['service']!s:<14} {v['mount']:<28} {v['usedMB']}MB / {v['sizeMB']}MB")
save('inventory.json', inv)
log('saved work/inventory.json and work/old_env_SECRET.json (contains secrets - never commit)')
