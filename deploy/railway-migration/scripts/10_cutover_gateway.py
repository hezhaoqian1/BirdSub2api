"""Phase D1: gateway cutover (~2 min of downtime on 10-29).
stop old gateway -> wait for old PG app connections to drain -> new PG replays up to the old final LSN
-> pg_promote -> new Redis REPLICAOF NO ONE -> start new gateway -> move the domain.
If the new gateway does not become healthy, the old one is redeployed (old DB was never touched)."""
from lib import *

M = load('map.json')
gw = CFG['gateway']
OLD = M['svc'][gw['service']]['old']
NEW = M['svc'][gw['service']]['new']

state = ssh('new', gw['postgres'], f'{PSQL} -c "SELECT pg_is_in_recovery(), (SELECT status FROM pg_stat_wal_receiver)"')
log('new PG:', state)
if 't|streaming' not in state:
    sys.exit('new Postgres is not a streaming standby - run 03_status.py and fix first')
new_before = deployments('new', NEW, 1)[0]['id']

# 1. stop old gateway (verified)
old_dep = stop_app('old', OLD)
save('cutover_gateway.json', {'old_deployment': old_dep['id']})

# 2. drain + sync point
for _ in range(20):
    n = ssh('old', gw['postgres'], f"{PSQL} -c \"SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend' "
                                   f"AND client_addr IS NOT NULL AND host(client_addr) NOT IN ('127.0.0.1','::1')\"").splitlines()[-1].strip()
    log('old PG remote app connections:', n)
    if n == '0':
        break
    time.sleep(3)
lsn = ssh('old', gw['postgres'], f'{PSQL} -c "SELECT pg_current_wal_lsn()"').splitlines()[-1].strip()
log('old final LSN', lsn)
for _ in range(30):
    r = ssh('new', gw['postgres'], f"{PSQL} -c \"SELECT pg_last_wal_replay_lsn() >= '{lsn}'::pg_lsn, pg_last_wal_replay_lsn()\"").splitlines()[-1]
    log('new caught up?', r)
    if r.startswith('t|'):
        break
    time.sleep(2)
else:
    sys.exit('standby did not catch up. Old gateway is STOPPED: investigate, or roll back with deploymentRedeploy of ' + old_dep['id'])

# 3. promote
log('promote:', ssh('new', gw['postgres'], f'{PSQL} -c "SELECT pg_promote(true, 60)" -c "SELECT pg_is_in_recovery()"').replace('\n', ' '))
log('redis:', ssh('new', gw['redis'], 'redis-cli -a "$REDIS_PASSWORD" --no-auth-warning REPLICAOF NO ONE').replace('\n', ' '))

# 4. start the new gateway for real (config change -> cached build, ~40s)
commit_patch('new', {'services': {NEW: {'deploy': {'startCommand': None, 'healthcheckPath': gw['healthcheck_path'],
                                                   'healthcheckTimeout': gw['healthcheck_timeout']}}}}, 'cutover: start gateway')
d = wait_new_deployment('new', NEW, new_before)
if d['status'] != 'SUCCESS':
    log('NEW GATEWAY NOT HEALTHY -> rolling back to the old gateway')
    log(gql('old', 'mutation($id:String!){ deploymentRedeploy(id:$id, usePreviousImageTag:true){ id status } }', {'id': old_dep['id']}))
    sys.exit('rolled back; the old DB was not modified')

# 5. domain
move_service_domain(OLD, NEW, M['temp_domains'][gw['service']], gw['domain'], gw['port'])
for _ in range(30):  # the edge returns 404 for ~20-30s after the rename
    st, body, _ = http('GET', f"https://{gw['domain']}{gw['healthcheck_path']}")
    log('health', st, body[:60])
    if st == 200:
        break
    time.sleep(3)

# 6. the old gateway must never come back on a push (it would write to the old DB)
gql('old', 'mutation($id:String!){ serviceDisconnect(id:$id){ id } }', {'id': OLD})
log('old gateway disconnected from GitHub. next: 12_long_entry.py, then 11_cutover_app.py <key>')
