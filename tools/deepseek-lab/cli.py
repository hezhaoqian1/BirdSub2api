"""CLI alternative; keys via prompt or named environment variable, never command flags."""
import argparse
import getpass
import os
import time

import engine
from transport import collect,endpoints

def main():
    p=argparse.ArgumentParser(description='DeepSeek Lab — API channel evaluation')
    p.add_argument('--url',required=True);p.add_argument('--models',default='');p.add_argument('--list-models',action='store_true')
    p.add_argument('--key-env',help='Environment variable NAME containing the API key')
    p.add_argument('--suites',default='speed,quality,reasoning,cache,community')
    p.add_argument('--seed',type=int,default=20261016);p.add_argument('--max-tokens',type=int,default=8192)
    p.add_argument('--repeats',type=int,default=1);p.add_argument('--concurrency',type=int,default=2)
    p.add_argument('--idle-timeout',type=int,default=90);p.add_argument('--deadline',type=int,default=240)
    args=p.parse_args();key=os.environ.get(args.key_env,'') if args.key_env else getpass.getpass('API Key (hidden): ')
    if not key:raise SystemExit('Missing API key')
    if args.list_models:
        r=collect(endpoints(args.url)[0],key,method='GET',idle=20,deadline=35)
        if r['http']!=200 or r['error']:raise SystemExit(f'Model discovery failed: HTTP {r["http"]}, {r["error"]}')
        for m in (r.get('document') or {}).get('data',[]):print(m.get('id',''))
        return
    if not args.models:raise SystemExit('Pass --models model1,model2 or --list-models')
    cfg=dict(targets=[dict(name='CLI',url=args.url,key=key,model=m.strip()) for m in args.models.split(',') if m.strip()],
             suites=args.suites.split(','),seed=args.seed,max_tokens=args.max_tokens,repeats=args.repeats,concurrency=args.concurrency,idle_timeout=args.idle_timeout,deadline=args.deadline)
    estimate=engine.validate(cfg);print(f'Plan: {estimate["calls"]} requests. No automatic retries.')
    job=engine.start(cfg);ident=job['id'];last=-1
    try:
        while True:
            state=engine.snapshot(engine.JOBS[ident])
            if state['completed']!=last:
                last=state['completed'];print(f'{last}/{state["planned"]} {state["status"]}',flush=True)
            if state['status']!='running':break
            time.sleep(1)
    except KeyboardInterrupt:
        engine.JOBS[ident]['stop'].set()
        while engine.JOBS[ident]['status']=='running':time.sleep(.2)
    print('Report:',engine.RESULTS/ident/'report.html')

if __name__=='__main__':main()
