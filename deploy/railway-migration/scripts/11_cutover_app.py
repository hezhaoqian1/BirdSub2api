"""Phase D3/D4: cut over one coincoin-style app (python app + MySQL).  usage: 11_cutover_app.py <key from CFG.apps>
Order (fixed after 10-29):
 1. stop old app (verified) + disconnect its repo
 2. move the custom domain FIRST and add the TXT via Cloudflare if CF token is set -> Railway's ~3 min
    verification overlaps with the data sync instead of adding to the downtime
 3. stop an unfinished precopy (it would overwrite tables later)
 4. delta: small tables full copy + window tables (REPLACE >= since) - apps on both sides are stopped
 5. start the new app
 6. move the *.up.railway.app domain
 7. background, lock-free backfill of the big log tables
 8. wait for the custom domain to verify"""
from lib import *

key = sys.argv[1] if len(sys.argv) > 1 else sys.exit('usage: 11_cutover_app.py <app key>')
app = CFG['apps'][key]
M = load('map.json')
inv = load('inventory.json')
pre = load('precopy.json')[app['mysql']]
OLD = M['svc'][app['service']]['old']
NEW = M['svc'][app['service']]['new']
src_host, src_port = pre['src']
# REPLACE window starts well before the precopy snapshot (timezone / late updates margin)
t0 = time.mktime(time.strptime(pre['t0_utc'], '%Y-%m-%d %H:%M:%S')) - CFG.get('mysql_window_margin_hours', 12) * 3600
since = time.strftime('%Y-%m-%d %H:%M:%S', time.localtime(t0))
new_before = deployments('new', NEW, 1)[0]['id']

# 1. stop old app
stop_app('old', OLD)
gql('old', 'mutation($id:String!){ serviceDisconnect(id:$id){ id } }', {'id': OLD})

# 2. custom domain first
cd = None
if app.get('custom_domain'):
    dom = app['custom_domain']
    old_cd = [d for d in domains('old', OLD)['customDomains'] if d['domain'] == dom]
    if old_cd:
        gql('old', 'mutation($id:String!){ customDomainDelete(id:$id) }', {'id': old_cd[0]['id']})
    for _ in range(30):
        if gql('new', 'query($d:String!){ customDomainAvailable(domain:$d){ available } }', {'d': dom})['customDomainAvailable']['available']:
            break
        time.sleep(2)
    cd = gql('new', 'mutation($i:CustomDomainCreateInput!){ customDomainCreate(input:$i){ id status { verificationDnsHost verificationToken'
                    ' dnsRecords { hostlabel requiredValue } } } }',
             {'i': {'domain': dom, 'environmentId': ids('new')[1], 'projectId': ids('new')[0], 'serviceId': NEW, 'targetPort': app['port']}})['customDomainCreate']
    zone = '.'.join(dom.split('.')[-2:])
    txt_name = f"{cd['status']['verificationDnsHost']}.{zone}"
    txt_value = cd['status']['verificationToken']
    cf_token = os.environ.get(CFG.get('cf_token_env_for_custom_domains', ''), '')
    if cf_token:
        z = cf_zone_id(zone, cf_token)
        r = cf('POST', f'/zones/{z}/dns_records', cf_token, {'type': 'TXT', 'name': txt_name, 'content': txt_value, 'ttl': 60})
        log('TXT added via Cloudflare:', r.get('success'), txt_name)
    else:
        print(f"\n!!! {dom} is DOWN until this TXT exists (add it in Cloudflare now; keep the old TXT):\n"
              f"    TXT  {txt_name}  {txt_value}\n    (optional CNAME {dom} -> {cd['status']['dnsRecords'][0]['requiredValue']}, keep proxy status)\n", flush=True)

# 3 + 4. data
db = app['mysql']
log('precopy:', ssh('new', db, script('kill_precopy.sh')).replace('\n', ' | '))
window = ' '.join(f'{t}:{c}' for t, c in CFG.get('mysql_window_tables', {}).items())
skip = ' '.join(CFG.get('mysql_background_tables', {}))
log('delta:\n' + ssh('new', db, f'echo {b64(script("mysql_delta.sh"))} | base64 -d > /tmp/delta.sh\n'
                                f'WINDOW_TABLES="{window}" SKIP_TABLES="{skip}" sh /tmp/delta.sh {src_host} {src_port} "{since}"', timeout=1800))
log('flush:', ssh('new', db, 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -N -e "SET GLOBAL innodb_flush_log_at_trx_commit=1; SELECT @@max_connections"'))

# 5. start the new app
commit_patch('new', {'services': {NEW: {'deploy': {'startCommand': None}}}}, f'cutover: start {app["service"]}')
d = wait_new_deployment('new', NEW, new_before)
if d['status'] != 'SUCCESS':
    sys.exit(f'new {app["service"]} failed to start - check its logs; old one is stopped (redeploy it to roll back)')

# 6. service domain
if app.get('service_domain'):
    move_service_domain(OLD, NEW, M['temp_domains'][app['service']], app['service_domain'], app['port'])

# 7. background backfill, lock-free
for t, c in CFG.get('mysql_background_tables', {}).items():
    log(f'backfill {t}:', ssh('new', db, f'echo {b64(script("mysql_backfill.sh"))} | base64 -d > /tmp/backfill.sh\n'
                                         f'nohup sh /tmp/backfill.sh {src_host} {src_port} {t} {c} "{since}" > /dev/null 2>&1 < /dev/null &\n'
                                         f'sleep 2; echo "started, log: /tmp/backfill_{t}.log"'))

# 8. wait for domains
urls = [f'https://{app[k]}/' for k in ('service_domain', 'custom_domain') if app.get(k)]
for _ in range(60):
    res = {u: http('GET', u, timeout=15)[0] for u in urls}
    log(res)
    if all(v == 200 for v in res.values()):
        break
    time.sleep(10)
log(f'done {key}. Check the app logs for errors, then run 20_verify.py')
