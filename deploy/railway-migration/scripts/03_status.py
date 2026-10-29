"""Phase C: is everything in sync and ready to cut over? Read-only."""
from lib import *

inv = load('inventory.json')
M = load('map.json')
gw = CFG['gateway']

print('== old Postgres (sender side)')
print(ssh('old', gw['postgres'], f'{PSQL} -c "SELECT application_name, state, pg_size_pretty(pg_current_wal_lsn()-replay_lsn) FROM pg_stat_replication"'
          f' -c "SELECT phase, pg_size_pretty(backup_streamed), pg_size_pretty(backup_total) FROM pg_stat_progress_basebackup"'))
print('== new Postgres (want: t | streaming | delay < 1s)')
print(ssh('new', gw['postgres'], f'{PSQL} -c "SELECT pg_is_in_recovery(), (SELECT status FROM pg_stat_wal_receiver), now()-pg_last_xact_replay_timestamp()"'))

for name, item in inv['services'].items():
    if item['image'].startswith('redis') and item['tcp_proxies']:
        print(f'== {name}:', ssh('new', name, 'redis-cli -a "$REDIS_PASSWORD" --no-auth-warning INFO replication | grep -E "^role|master_link_status"').replace('\n', ' | '))

for app in CFG['apps'].values():
    db = app['mysql']
    print(f'== {db} precopy:', ssh('new', db, 'cat /tmp/precopy.done 2>/dev/null || echo running; du -sh /var/lib/mysql/railway | cut -f1').replace('\n', ' | '))

print('== apps (want SUCCESS = built and sleeping)')
for name in CFG['sleep_apps']:
    d = deployments('new', M['svc'][name]['new'], 1)[0]
    print(f'   {name:<14} {d["status"]} start={((d.get("meta") or {}).get("serviceManifest") or {}).get("deploy", {}).get("startCommand")}')
