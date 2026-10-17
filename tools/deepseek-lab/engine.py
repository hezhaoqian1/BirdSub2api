import collections
import concurrent.futures
import datetime
import hashlib
import html
import json
import math
import os
import pathlib
import statistics
import threading
import time
import uuid

from suites import VERSION, build_plan, flag_result
from transport import collect, endpoints, scrub

ROOT=pathlib.Path(__file__).parent
RESULTS=pathlib.Path(os.environ.get('LAB_RESULTS_DIR',str(ROOT/'results')))
JOBS={}; LOCK=threading.RLock()


def now():return datetime.datetime.now().astimezone().isoformat()
def replace_snapshot(temp,target):
    # Windows readers/virus scanners can briefly deny replacing an open file.
    for attempt in range(6):
        try:temp.replace(target);return
        except PermissionError:
            if attempt==5:raise
            time.sleep(.05*2**attempt)
def median(values):return statistics.median(values) if values else None
def percentile(values,p):
    if not values:return None
    return sorted(values)[max(0,math.ceil(len(values)*p)-1)]


def validate(cfg):
    targets=cfg.get('targets',[])
    if not targets or len(targets)>8:raise ValueError('请选择1至8个渠道/模型组合')
    for t in targets:
        endpoints(t.get('url',''))
        if not isinstance(t.get('key'),str) or not t['key'].strip():raise ValueError('API Key 不能为空')
        if not isinstance(t.get('model'),str) or not t['model'].strip():raise ValueError('模型名称不能为空')
        if len(t['model'])>200 or len(t.get('name',''))>100:raise ValueError('模型名或渠道名称过长')
    limits={'max_tokens':(128,32768,8192),'repeats':(1,5,1),'concurrency':(1,4,2),'idle_timeout':(5,300,90),'deadline':(10,600,240),'seed':(0,2147483647,20261016),'cache_delay':(0,60,3)}
    for k,(low,high,default) in limits.items():
        cfg[k]=int(cfg.get(k,default))
        if not low<=cfg[k]<=high:raise ValueError(f'{k} 必须在 {low}–{high} 之间')
    if cfg.get('thinking','enabled') not in ('enabled','disabled','omit'):raise ValueError('无效 thinking 设置')
    if cfg.get('effort','high') not in ('high','max','low','medium',''):raise ValueError('无效 reasoning_effort')
    prices=cfg.get('prices') or {}
    for k,v in prices.items():
        if k not in ('miss','hit','output') or not isinstance(v,(int,float)) or not math.isfinite(v) or v<0:raise ValueError('价格必须为非负有限数')
    plans=[build_plan(cfg,'preview-'+str(i)) for i in range(len(targets))]
    count=sum(map(len,plans))
    if count>400:raise ValueError('一次最多400个请求；请减少模型/轮次/套件')
    maximum=sum(int(t['payload'].get('max_tokens',t['payload'].get('max_completion_tokens',cfg['max_tokens']))) for p in plans for t in p)
    return dict(calls=count,per_target=len(plans[0]),completion_token_budget=maximum,
                notes=['预算是各请求上限之和，不是预计费用。原始请求未指定上限时使用设置值估计，实际由上游决定。','不自动重试；鉴权/余额错误立即停止该组合，连续三次传输或HTTP错误停止。','同一模型顺序执行；并发只作用于不同渠道/模型组合。'])


def summarize(rows):
    groups=collections.defaultdict(list)
    for r in rows:groups[r['target_id']].append(r)
    output=[]
    for tid,rs in groups.items():
        q=[r for r in rs if r['suite'] in ('quality','community')]
        successes=[r for r in rs if r['complete']]
        speed=[r for r in rs if r['suite']=='speed' and r['delivered_correct']]
        cache=[r for r in rs if r['suite']=='cache']
        hashes=collections.defaultdict(list)
        for r in rs:hashes[r['request_hash']].append(r)
        drift=[]
        for h,xs in hashes.items():
            counts={r['metrics']['prompt_tokens'] for r in xs if r['metrics']['prompt_tokens'] is not None}
            if len(counts)>1:drift.append(dict(request_hash=h,prompt_tokens=sorted(counts),case_ids=[r['id'] for r in xs]))
        labels=collections.Counter(m for r in rs for m in r['returned_models'])
        warm=[r for r in cache if r['task'] in ('A-warm1','A-warm2') and r['metrics']['cache_percent'] is not None]
        valid_warm=[r for r in warm if r['delivered_correct']]
        def weighted(xs):
            den=sum(r['metrics']['prompt_tokens'] for r in xs)
            return 100*sum(r['metrics']['cache_tokens'] for r in xs)/den if den else None
        correct=sum(r['delivered_correct'] for r in q);n=len(q)
        interval=None
        if n:
            z=1.96;prop=correct/n;mid=(prop+z*z/(2*n))/(1+z*z/n);half=z*math.sqrt(prop*(1-prop)/n+z*z/(4*n*n))/(1+z*z/n)
            interval=[100*(mid-half),100*(mid+half)]
        output.append(dict(target_id=tid,name=rs[0]['target_name'],model=rs[0]['requested_model'],calls=len(rs),complete=len(successes),
            quality_correct=correct,quality_total=n,quality_percent=100*correct/n if n else None,quality_wilson95=interval,
            quality_semantic_correct=sum(r['grade']['correct'] is True and r['http']==200 and r['finish']=='stop' and r['content']!=r['reasoning'] for r in q),
            quality_format_correct=sum(r['delivered_correct'] and r['grade']['format_ok'] is True for r in q),
            quality_manual_reviews=sum(bool(r.get('manual_review')) for r in q),
            quality_incomplete_marker=sum(any(f['code']=='incomplete_final_marker' for f in r['flags']) for r in q),
            quality_unique=len({r['task'] for r in q}),quality_note='描述性样本区间；重复题/公开题并非独立随机样本，不代表模型整体智力',
            speed_samples=len(speed),median_first_content_s=median([r['first_content_s'] for r in speed if r['first_content_s'] is not None]),
            median_first_reasoning_s=median([r['first_reasoning_s'] for r in speed if r['first_reasoning_s'] is not None]),
            median_total_s=median([r['total_s'] for r in speed]),p95_total_s=percentile([r['total_s'] for r in speed],.95),
            median_reported_tps=median([r['reported_completion_tps'] for r in speed if r['reported_completion_tps'] is not None]),
            labels=dict(labels),flags=dict(collections.Counter(f['code'] for r in rs for f in r['flags'])),prompt_token_drift=drift,
            warm_cache_reported=weighted(warm),warm_cache_correct=weighted(valid_warm),warm_cache_samples=len(warm),
            warm_cache_missing=sum(r['metrics']['cache_percent'] is None for r in cache if r['task'] in ('A-warm1','A-warm2')),
            cache_cases=[dict(id=r['id'],task=r['task'],repeat=r['repeat'],correct=r['delivered_correct'],cache=r['metrics']['cache_percent'],first=r['first_content_s'],total=r['total_s']) for r in cache],
            suites={s:dict(calls=len(xs),complete=sum(r['complete'] for r in xs),correct=sum(r['delivered_correct'] for r in xs))
                    for s in sorted({r['suite'] for r in rs}) for xs in [[r for r in rs if r['suite']==s]]}))
    return output


def snapshot(job, full=False):
    with LOCK:
        d={k:job[k] for k in ('id','started_at','status','planned','completed','active','notices','config','targets')}
        d['summary']=summarize(job['rows']); d['finished_at']=job.get('finished_at')
        if full:d['rows']=list(job['rows'])
        else:
            d['live_rows']=[{k:r.get(k) for k in ('id','target_id','target_name','requested_model','task','suite','grade','delivered_correct','total_s','flags')} for r in job['rows'][-12:]]
        return d


def save(job):
    final=job['status'] in ('complete','cancelled','failed','interrupted')
    data=snapshot(job,final); directory=RESULTS/job['id'];directory.mkdir(parents=True,exist_ok=True)
    cases=directory/'cases';cases.mkdir(exist_ok=True)
    # Persist each raw stream once during a run, instead of rewriting hundreds
    # of megabytes after every request. Final exports retain the full evidence.
    for row in job['rows']:
        target=cases/(row['id']+'.json')
        if final or not target.exists():
            temp_case=target.with_suffix('.json.tmp')
            temp_case.write_text(json.dumps(row,ensure_ascii=False),encoding='utf-8');replace_snapshot(temp_case,target)
    if not final:data['completed_case_ids']=[r['id'] for r in job['rows']]
    # Atomic snapshots let the UI and exports read while a run progresses.
    temp=directory/'report.json.tmp';temp.write_text(json.dumps(data,ensure_ascii=False,indent=None if final else 2),encoding='utf-8');replace_snapshot(temp,directory/'report.json')
    if final:
        view=snapshot(job);view['live_rows']=[{k:r.get(k) for k in ('id','target_id','target_name','requested_model','task','suite','grade','delivered_correct','total_s','flags')} for r in job['rows']]
        view_temp=directory/'view.json.tmp';view_temp.write_text(json.dumps(view,ensure_ascii=False),encoding='utf-8');replace_snapshot(view_temp,directory/'view.json')
        css=(ROOT/'static/style.css').read_text(encoding='utf-8');js=(ROOT/'static/report.js').read_text(encoding='utf-8')
        encoded=json.dumps(data,ensure_ascii=False).replace('<','\\u003c').replace('\u2028','\\u2028').replace('\u2029','\\u2029')
        page='<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>DeepSeek Lab 测试报告</title><style>'+css+'</style><body><main><h1>DeepSeek Lab · 测试报告</h1><div id="report"></div></main><script>'+js+'</script><script>renderReport('+encoded+');</script></body></html>'
        (directory/'report.html').write_text(page,encoding='utf-8')


def start(cfg):
    estimate=validate(cfg)
    with LOCK:
        if any(j['status']=='running' for j in JOBS.values()):raise ValueError('已有测试运行中，请完成或停止后再开始')
        ident=datetime.datetime.now().strftime('%Y%m%d-%H%M%S')+'-'+uuid.uuid4().hex[:8]
        safe={k:v for k,v in cfg.items() if k!='targets'}
        # Custom bodies are stored per-case, not in UI run configuration.
        raw=safe.pop('custom_body',None)
        if raw:safe['custom_body_sha256']=hashlib.sha256(json.dumps(raw,sort_keys=True,ensure_ascii=False).encode()).hexdigest()
        targets=[dict(id=str(i),name=t.get('name') or '渠道 '+str(i+1),url=t['url'],model=t['model']) for i,t in enumerate(cfg['targets'])]
        for target in cfg['targets']:
            safe=scrub(safe,target['key']);targets=scrub(targets,target['key'])
        safe['version']=VERSION
        safe['comparison_signature']=hashlib.sha256(json.dumps({k:v for k,v in safe.items() if k not in ('concurrency',)},sort_keys=True).encode()).hexdigest()
        job=dict(id=ident,started_at=now(),status='running',planned=estimate['calls'],completed=0,active={},notices=[],config=safe,targets=targets,rows=[],stop=threading.Event())
        JOBS[ident]=job
        save(job)
    thread=threading.Thread(target=run,args=(job,cfg),daemon=True);thread.start()
    return snapshot(job)


def run(job,cfg):
    keys=[t['key'] for t in cfg['targets']]
    def clean(x):
        for key in keys:x=scrub(x,key)
        return x
    def worker(i,target):
        tid=str(i);failures=0
        plan=build_plan(cfg,job['id']+'-'+tid)
        for seq,t in enumerate(plan,1):
            if job['stop'].wait(t.get('delay_before',0)):break
            with LOCK:job['active'][tid]=t['name']+f' ({seq}/{len(plan)})'
            payload=dict(t['payload'],model=target['model']);t['payload']=payload
            r=collect(endpoints(target['url'])[1],target['key'],payload,job['stop'],cfg['idle_timeout'],cfg['deadline'])
            r.update(id=tid+'-'+str(seq),target_id=tid,target_name=target.get('name') or '渠道 '+str(i+1),requested_model=target['model'],task=t['name'],suite=t['suite'],category=t.get('category'),repeat=t['repeat'],expected=t.get('expected'),source=t.get('source'),request=payload)
            flag_result(r,t)
            r=clean(r)
            with LOCK:
                job['rows'].append(r);job['completed']+=1
                save(job)
            failures=failures+1 if (r['error'] or r['http']!=200 or r.get('response_error')) else 0
            if r['http'] in (401,402,403) or failures>=3:
                with LOCK:job['notices'].append(clean(f'{target.get("name") or tid} / {target["model"]} 已熔断，剩余测试未执行；失败记录保留。'))
                break
        with LOCK:job['active'].pop(tid,None)
    final_status='complete'
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=cfg['concurrency']) as pool:
            futures=[pool.submit(worker,i,t) for i,t in enumerate(cfg['targets'])]
            for f in concurrent.futures.as_completed(futures):
                try:f.result()
                except Exception:
                    job['stop'].set();raise
        final_status='cancelled' if job['stop'].is_set() else 'complete'
    except Exception as exc:
        job['stop'].set();final_status='failed';job['notices'].append('运行器异常：'+type(exc).__name__+'；'+clean(str(exc))[:300])
    finally:
        with LOCK:
            job['status']=final_status;job['finished_at']=now();job['active']={};save(job)
        for t in cfg['targets']:t['key']=''


def history():
    output=[]
    if not RESULTS.exists():return output
    for p in sorted(RESULTS.glob('*/report.json'),reverse=True)[:100]:
        try:
            view=p.parent/'view.json'
            d=json.loads((view if view.exists() else p).read_text(encoding='utf-8'))
            output.append({k:d.get(k) for k in ('id','started_at','status','completed','planned','summary','config','targets')})
        except (ValueError,OSError):continue
    with LOCK:
        running={i:snapshot(j) for i,j in JOBS.items() if j['status']=='running'}
    output=[d for d in output if d['id'] not in running]
    return sorted(list(running.values())+output,key=lambda d:d['started_at'],reverse=True)[:100]


def review_case(ident,case_id,verdict,note):
    import re
    if not re.fullmatch(r'[0-9a-f-]+',ident):raise ValueError('无效报告ID')
    if verdict not in ('correct','incorrect','reset'):raise ValueError('无效复核结论')
    if not isinstance(note,str) or not 3<=len(note.strip())<=2000:raise ValueError('请填写3–2000字的复核理由')
    with LOCK:
        job=JOBS.get(ident)
        if job and job['status']=='running':raise ValueError('请在本轮结束后复核，避免修改运行中的评分')
        directory=RESULTS/ident
        if job is None:job=json.loads((directory/'report.json').read_text(encoding='utf-8'))
        if job['status']=='running':raise ValueError('该报告仍在运行中')
        row=next((r for r in job['rows'] if r['id']==case_id),None)
        if row is None:raise ValueError('请求记录不存在')
        row.setdefault('grade_before_review',dict(row['grade']))
        review=dict(case_id=case_id,verdict=verdict,note=note.strip(),at=now())
        row['manual_review']=review
        row['grade']=dict(row['grade_before_review'])
        if verdict!='reset':row['grade'].update(correct=verdict=='correct',status='manual_pass' if verdict=='correct' else 'manual_mismatch')
        row['flags']=[f for f in row['flags'] if f['code']!='format_noncompliant']
        if row['grade']['correct'] and row['grade']['format_ok'] is False:
            row['flags'].append(dict(code='format_noncompliant',detail='语义答案经复核正确，但输出格式不符',severity='info'))
        row['delivered_correct']=bool(row['complete'] and row['grade']['correct'] is True and row['content']!=row['reasoning'])
        ledger=directory/'manual_reviews.json'
        reviews=json.loads(ledger.read_text(encoding='utf-8')) if ledger.exists() else []
        reviews.append(review);ledger.write_text(json.dumps(reviews,ensure_ascii=False,indent=2),encoding='utf-8')
        save(job)
        view=snapshot(job);view['live_rows']=[{k:r.get(k) for k in ('id','target_id','target_name','requested_model','task','suite','grade','delivered_correct','total_s','flags')} for r in job['rows']]
        return view
