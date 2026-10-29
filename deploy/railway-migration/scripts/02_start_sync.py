"""Phase B (cont.): start data sync that 01 does not start by itself.
- Redis with an old TCP proxy -> REPLICAOF (cache-only Redis without a proxy is not migrated)
- each app MySQL -> background precopy (nohup) from the old MySQL's TCP proxy
- replay the old MySQL's SET PERSIST variables (a logical dump does not carry them; 10-29 max_connections)
The new Postgres streams on its own (boot script)."""
from lib import *

inv = load('inventory.json')
M = load('map.json')

for name, item in inv['services'].items():
    if not item['image'].startswith('redis'):
        continue
    if not item['tcp_proxies']:
        log(f'redis {name}: no TCP proxy on the old side -> not migrated (cache only)')
        continue
    t = item['tcp_proxies'][0]
    out = ssh('new', name, 'R="redis-cli -a $REDIS_PASSWORD --no-auth-warning"\n'
              '$R CONFIG SET masterauth "$REDIS_PASSWORD"\n'
              f'$R REPLICAOF {t["domain"]} {t["proxyPort"]}\nsleep 15\n'
              '$R INFO replication | grep -E "^role|master_link_status"; $R DBSIZE')
    log(f'redis {name} -> replica of {t["domain"]}:{t["proxyPort"]}:', out.replace('\n', ' | '))

pre = load('precopy.json', {})
for key, app in CFG['apps'].items():
    db = app['mysql']
    t = inv['services'][db]['tcp_proxies'][0]
    out = ssh('new', db, f'echo {b64(script("mysql_precopy.sh"))} | base64 -d > /tmp/precopy.sh\n'
              'rm -f /tmp/precopy.done\n'
              f'nohup sh /tmp/precopy.sh {t["domain"]} {t["proxyPort"]} > /tmp/precopy.log 2>&1 < /dev/null &\n'
              'sleep 5; cat /tmp/precopy.t0')
    pre[db] = {'t0_utc': out.splitlines()[-1].strip(), 'src': [t['domain'], t['proxyPort']]}
    log(f'mysql {db}: precopy started, snapshot t0 (UTC) = {pre[db]["t0_utc"]}')
    persisted = {k: v for k, v in inv['services'][db].get('mysql_params', {}).get('persisted', {}).items() if k != 'server_id'}
    if persisted:
        sets = '; '.join(f"SET PERSIST {k}='{v}'" if not v.isdigit() else f'SET PERSIST {k}={v}' for k, v in persisted.items())
        log(f'mysql {db}: replay {persisted} ->', ssh('new', db, f'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -e "{sets}" && echo ok'))
save('precopy.json', pre)
log('next: python -X utf8 scripts/03_status.py  (repeat until Postgres is streaming)')
