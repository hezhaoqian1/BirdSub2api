"""Phase F: cleanup after a successful cutover. usage: 30_cleanup.py [--remove-old-long-entry]
- old Postgres: drop the replication slot (an unused slot makes WAL pile up) and the pg_hba rule
- old apps: make sure they are really stopped and disconnected from GitHub
- new Postgres: clear leftover standby settings (primary_conninfo holds the old DB password) with a reload;
  stage the start-command / MIG_* cleanup with skipDeploys so it applies on the next normal restart.
  MIG_BOOT_B64 stays until then: a restart of the CURRENT deployment still runs the boot script."""
from lib import *

M = load('map.json')
gw = CFG['gateway']

log('old Postgres:', ssh('old', gw['postgres'], 'sed -i "/# mig-newacct$/d" "$PGDATA/pg_hba.conf"\n'
                         f"{PSQL} -c \"SELECT pg_drop_replication_slot('{SLOT}') FROM pg_replication_slots WHERE slot_name='{SLOT}'\""
                         ' -c "SELECT pg_reload_conf()" -c "SELECT count(*) FROM pg_replication_slots"').replace('\n', ' | '))

oc = env_config('old')
targets = list(CFG['sleep_apps']) + ([CFG['long_entry']['service']] if '--remove-old-long-entry' in sys.argv else [])
for name in targets:
    sid = M['svc'][name]['old']
    dep = (deployments('old', sid, 1) or [None])[0]
    if dep and any(i['status'] == 'RUNNING' for i in dep['instances']):
        stop_app('old', sid)
    if (oc['services'].get(sid, {}).get('source') or {}).get('repo'):
        gql('old', 'mutation($id:String!){ serviceDisconnect(id:$id){ id } }', {'id': sid})
    log(f'old {name}: stopped and disconnected')

pg = M['svc'][gw['postgres']]['new']
log('new Postgres:', ssh('new', gw['postgres'], f'{PSQL} -c "ALTER SYSTEM RESET primary_conninfo" -c "ALTER SYSTEM RESET primary_slot_name"'
                         ' -c "SELECT pg_reload_conf()"; grep -c primary_conninfo "$PGDATA/postgresql.auto.conf"').replace('\n', ' | '))
commit_patch('new', {'services': {pg: {'deploy': {'startCommand': None},
                                       'variables': {k: None for k in ('MIG_SRC_HOST', 'MIG_SRC_PORT', 'MIG_SRC_PASSWORD', 'MIG_SLOT')}}}},
             'migration cleanup (applies on next restart)', skip_deploys=True)
log('staged. After the next Postgres deploy, delete MIG_BOOT_B64. Keep the old databases a few days as a backup.')
for app in CFG['apps'].values():
    for t in CFG.get('mysql_background_tables', {}):
        log(f'{app["mysql"]} backfill {t}:', ssh('new', app['mysql'], f'cat /tmp/backfill_{t}.done 2>/dev/null || tail -1 /tmp/backfill_{t}.log 2>/dev/null || echo "not started"'))
