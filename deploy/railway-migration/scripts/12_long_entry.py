"""Phase D2: point the long-task entry DNS at the new caddy-long TCP proxy.
The TCP proxy PORT is assigned by Railway and always changes -> send the new URL to long-task customers."""
from lib import *

M = load('map.json')
le = CFG['long_entry']
old_cfg = load('old_env_SECRET.json')
cf_token = old_cfg['services'][M['svc'][le['service']]['old']]['variables']['CF_API_TOKEN']['value']
tcp = tcp_proxies('new', M['svc'][le['service']]['new'])[0]

z = cf_zone_id(le['cf_zone'], cf_token)
rec = cf('GET', f"/zones/{z}/dns_records?name={le['dns_record']}", cf_token)['result'][0]
log('before:', rec['type'], rec['content'], 'proxied', rec['proxied'])
out = cf('PATCH', f"/zones/{z}/dns_records/{rec['id']}", cf_token, {'content': tcp['domain'], 'proxied': False})  # must stay DNS-only
log('after :', out['result']['type'], out['result']['content'], 'proxied', out['result']['proxied'])

url = f"https://{le['dns_record']}:{tcp['proxyPort']}"
for _ in range(10):
    st, _, _ = http('GET', url + '/v1/models', timeout=20)
    log(url, '/v1/models ->', st, '(401 = OK, no key sent)')
    if st == 401:
        break
    time.sleep(6)
print(f'\nNEW LONG-TASK URL: {url}\nTell every long-task customer; the old port stops working now.')
