"""Versioned plans: independent expected answers; no answer key sent to a model."""
import copy
import hashlib
import itertools
import json
import math
import pathlib
import random
import re
from fractions import Fraction

VERSION = '1.2.1'
ROOT = pathlib.Path(__file__).parent
SUITES = {
    'speed': ('速度与流式协议', '3 次相同输出任务；区分首思考、首正文、总耗时、尾部等待'),
    'quality': ('随机可验证推理', '8 道由本地枚举/计算产生标答的题，避免仅靠常见公开题'),
    'reasoning': ('思考与输出异常', '同题 4 种参数：开启、关闭、关闭+max、低预算；含冲突参数诊断'),
    'cache': ('缓存与内容对照', '12 次顺序请求：重复、改尾部、改前缀、全新数据、恢复、开启思考'),
    'community': ('社区公开题', 'BBH 12 + MATH-500 8 + BBEH 4；固定小样本，不是官方榜单'),
    'custom': ('原始请求复现', '原样保留请求体参数，只替换所选模型；可选填写标准答案'),
}


def task(name, suite, prompt, expected=None, kind='exact', **extra):
    return dict(name=name, suite=suite, prompt=prompt, expected=expected, kind=kind, **extra)


def quality(seed):
    rng = random.Random(seed)
    n = rng.randint(10, 14); k = 3
    count = sum(all((a-b) % n not in (1, n-1) for a,b in itertools.combinations(xs,2)) for xs in itertools.combinations(range(n), k))
    digit_sum = rng.randint(10, 18); divisor = rng.choice([7,11,13])
    nums = sum(len(set(str(i)))==3 and sum(map(int,str(i)))==digit_sum and i%divisor==0 for i in range(100,1000))
    size = rng.choice([6,7]); fixed = 2
    permutations = sum(sum(i==v for i,v in enumerate(p))==fixed for p in itertools.permutations(range(size)))
    threshold = rng.randint(13,16)
    dice = [x for x in itertools.product(range(1,7),repeat=3) if 6 in x]
    probability = Fraction(sum(sum(x)>=threshold for x in dice),len(dice))
    durations=[rng.randint(1,5) for _ in range(4)]; deadlines=[rng.randint(3,10) for _ in range(4)]
    costs=[]
    for p in itertools.permutations(range(4)):
        t=0;c=0
        for j in p:t+=durations[j];c+=max(0,t-deadlines[j])
        costs.append(c)
    exponent=rng.randint(1900,2200)
    questions=[
        (f'圆环有{n}个编号座位，首尾相邻，选{ k }个，两两不相邻，共多少种选法？',str(count)),
        (f'多少个三位正整数的数字互不相同、数字之和为{digit_sum}，且能被{divisor}整除？',str(nums)),
        (f'将1至{size}排列，恰好有{fixed}个数在原位置，共多少个排列？',str(permutations)),
        (f'三枚独立公平六面骰，已知至少一枚为6，点数之和至少为{threshold}的条件概率？最简分数。',str(probability)),
        (f'单机时刻0开始无抢占安排ABCD，每次一个作业。耗时{durations}，截止时间{deadlines}。拖期=max(0,完成时间-截止时间)，最小总拖期？',str(min(costs))),
        (f'7的{exponent}次方除以1000的余数？',str(pow(7,exponent,1000))),
        ('Python3: x=[[]]*3; x[0].append(1); x[1]=x[1]+[2]; x[2].append(3)。最终x是什么？用JSON数组表示。','[[1,3],[1,2],[1,3]]'),
        ('A、B、C恰有一人犯罪。A说B犯罪，B说C犯罪，C说B刚才说的是假话。恰有两句话真，罪犯是谁？只用字母。','B'),
    ]
    return [task('随机推理-'+str(i+1),'quality',q+'\n最后一行输出 FINAL: 答案。',a,'final',source='本地枚举/精确计算；种子 '+str(seed)) for i,(q,a) in enumerate(questions)]


def build_plan(config, namespace):
    seed=int(config.get('seed',20261016)); budget=int(config.get('max_tokens',8192))
    thinking=config.get('thinking','enabled'); effort=config.get('effort','high')
    plan=[]
    selected=config.get('suites',['speed','quality','reasoning','cache'])
    if 'speed' in selected:
        for i in range(3):
            plan.append(task('速度-'+str(i+1),'speed','只输出从0到127的整数JSON数组，不要Markdown或解释。',list(range(128)),'json',thinking='disabled',effort=None))
    if 'quality' in selected: plan.extend(quality(seed))
    if 'reasoning' in selected:
        q='2026年10月16日的下一天是哪一天？只输出 YYYY-MM-DD。'
        for name,t,e,b in [('开启-high','enabled','high',budget),('关闭-无effort','disabled',None,budget),('关闭-max冲突诊断','disabled','max',budget),('开启-低预算诊断','enabled','high',128)]:
            plan.append(task(name,'reasoning',q,'2026-10-17','exact',thinking=t,effort=e,budget=b))
    if 'cache' in selected:
        rng=random.Random(seed); values=[rng.randrange(1000,9000) for _ in range(192)]
        def payload(prefix, vals, indices):
            data='\n'.join(f'item={i:03d}; quantity={v}; warehouse=W{i%11}; label=sample-{i:03d}; verified=yes; color=blue.' for i,v in enumerate(vals))
            return prefix+'\n'+data+f'\n仅输出 item {indices[0]:03d} 与 item {indices[1]:03d} 的 quantity 之和，不要解释。'
        a=payload(namespace+'-A',values,(17,91)); b=payload(namespace+'-A',values,(18,92))
        c=payload(namespace+'-C',values,(17,91)); new=[v+101 for v in values]; d=payload(namespace+'-D',new,(17,91))
        variants=[('A-cold',a,values[17]+values[91]),('A-warm1',a,values[17]+values[91]),('A-warm2',a,values[17]+values[91]),
                  ('B-tail1',b,values[18]+values[92]),('B-tail2',b,values[18]+values[92]),
                  ('C-prefix1',c,values[17]+values[91]),('C-prefix2',c,values[17]+values[91]),
                  ('D-new1',d,new[17]+new[91]),('D-new2',d,new[17]+new[91]),('A-restore',a,values[17]+values[91]),
                  ('E-thinking1',a,values[17]+values[91]),('E-thinking2',a,values[17]+values[91])]
        for name,p,answer in variants:
            enabled=name.startswith('E-')
            plan.append(task(name,'cache',p,str(answer),'number',thinking='enabled' if enabled else 'disabled',effort='high' if enabled else None,
                             cache_variant=name,delay_before=0 if name=='A-cold' else float(config.get('cache_delay',3))))
    if 'community' in selected:
        data=json.loads((ROOT/'data/community.json').read_text(encoding='utf-8'))
        plan.extend(copy.deepcopy(data['questions']))
    if 'custom' in selected:
        raw=config.get('custom_body')
        if isinstance(raw,str): raw=json.loads(raw)
        if not isinstance(raw,dict) or not isinstance(raw.get('messages'),list) or not raw['messages']:
            raise ValueError('原始请求必须是包含非空 messages 数组的 JSON 对象；不可使用被截断的日志')
        if raw.get('n',1)!=1: raise ValueError('当前只评估 n=1；请将原始请求 n 设置为1后再运行')
        if len(json.dumps(raw,ensure_ascii=False))>2_000_000: raise ValueError('原始请求超过2MB限制')
        plan.append(dict(name='原始请求复现',suite='custom',kind='exact',expected=config.get('custom_expected') or None,payload=raw))
    expanded=[]
    for repeat in range(int(config.get('repeats',1))):
        for src in plan:
            t=copy.deepcopy(src);t['repeat']=repeat+1
            if 'payload' not in t:
                p=dict(messages=[{'role':'user','content':t['prompt']}],max_tokens=t.get('budget',budget),stream=True,stream_options={'include_usage':True})
                mode=t.get('thinking',thinking);e=t.get('effort',effort)
                if mode!='omit':p['thinking']={'type':mode}
                if e and mode!='omit' and (mode!='disabled' or 'effort' in t):p['reasoning_effort']=e
                t['payload']=p
            expanded.append(t)
    if not expanded:raise ValueError('请至少选择一个测试套件')
    return expanded


def boxed(text):
    values=[]
    for m in re.finditer(r'\\boxed\s*\{',text):
        pos=m.end();begin=pos;depth=1
        while pos<len(text) and depth:
            depth+=(text[pos]=='{')-(text[pos]=='}');pos+=1
        if depth==0: values.append(text[begin:pos-1])
    return values


def normal(s):
    return re.sub(r'\s+','',str(s)).replace('\\left','').replace('\\right','').replace('^{\\circ}','').replace('^\\circ','').strip('$').rstrip('.')


def grade(content, task):
    expected=task.get('expected')
    if expected is None:return dict(status='manual',correct=None,prediction=None,format_ok=None)
    raw=content.strip();kind=task.get('kind','exact');pred=raw;fmt=True
    if kind=='number':
        fmt=bool(re.fullmatch(r'-?\d+',raw))
        pred=raw if fmt else None
        if pred is None:
            equation=re.fullmatch(r'\s*(-?\d+)\s*\+\s*(-?\d+)\s*=\s*(?:\*\*)?(-?\d+)(?:\*\*)?\s*',raw)
            if equation and int(equation[1])+int(equation[2])==int(equation[3]):pred=equation[3]
            else:
                answer=re.fullmatch(r'(?:答案\s*[:：]\s*|FINAL\s*:\s*)?(?:\*\*)?(-?\d+)(?:\*\*)?[。.]?',raw,re.I)
                if answer:pred=answer[1]
        return dict(status='pass' if pred is not None and normal(pred)==normal(expected) else 'mismatch',
                    correct=pred is not None and normal(pred)==normal(expected),prediction=pred,format_ok=fmt)
    if kind=='json':
        try: pred=json.loads(raw)
        except ValueError:
            fmt=False
            try: pred=json.loads(re.sub(r'^```(?:json)?\s*|\s*```$','',raw))
            except ValueError: return dict(status='unparsed',correct=False,prediction=None,format_ok=False)
        ok=pred==expected
    else:
        if kind=='boxed':
            found=boxed(raw);pred=found[-1] if found else None;fmt=bool(found)
        elif kind=='final':
            found=re.findall(r'(?im)^\s*(?:FINAL\s*:|The answer is\s*:?)\s*(.+?)\s*$',raw)
            pred=found[-1].strip().rstrip('.') if found else (raw if normal(raw)==normal(expected) else None);fmt=bool(found)
        if pred is None:return dict(status='unparsed',correct=False,prediction=None,format_ok=fmt)
        # Parentheses on a single option are semantic-equivalent, but keep strict-format status.
        if re.fullmatch(r'\(?[A-Z]\)?',str(expected)) and re.fullmatch(r'\(?[A-Z]\)?',str(pred)):
            ok=str(pred).strip('()')==str(expected).strip('()');fmt=fmt and str(pred)==str(expected)
        else: ok=normal(pred)==normal(expected)
        if not ok and kind=='boxed':
            # Conservative rational equivalence. Never eval model text or silently judge arbitrary algebra.
            def fraction(s):
                s=normal(s);m=re.fullmatch(r'\\(?:d?frac)\{(-?\d+)\}\{(\d+)\}',s)
                if m:return Fraction(int(m[1]),int(m[2]))
                if re.fullmatch(r'-?\d+(?:/\d+|\.\d+)?',s):return Fraction(s)
                return None
            try:
                a,b=fraction(pred),fraction(expected);ok=a is not None and b is not None and a==b
            except (ValueError,ZeroDivisionError):pass
    return dict(status='pass' if ok else 'review' if kind=='boxed' else 'mismatch',correct=ok,prediction=pred,format_ok=fmt)


def flag_result(r, t):
    flags=[]
    def flag(code,detail,severity='warning'):flags.append(dict(code=code,detail=detail,severity=severity))
    if r.get('error'):flag('transport_error',r['error'],'error')
    if r['http'] is not None and r['http']!=200:flag('http_error',str(r['http']),'error')
    if r.get('response_error'):flag('upstream_error','响应体包含 error','error')
    if r['finish']=='length':flag('budget_exhausted','输出预算耗尽；不能把截断当作推理能力错误','error')
    if r['http']==200 and not r['content'].strip():flag('empty_answer','没有交付正文，即使有思考也不算答题成功','error')
    if r['reasoning'] and r['reasoning']==r['content']:flag('reasoning_copied_to_content','正文与思考完全相同，疑似字段复制/兜底','error')
    if re.search(r'(?:FINAL\s*:?|The answer is\s*:?)\s*$',r['content'],re.I):
        flag('incomplete_final_marker','正文停在答案标记处；即使 finish=stop 也需检查是否实际交付答案')
    elif '\\boxed' in r['content'] and len(boxed(r['content'])) < len(re.findall(r'\\boxed\b',r['content'])):
        flag('incomplete_final_marker','正文包含未闭合的 boxed 答案；即使 finish=stop 也需检查末尾是否被截断')
    if re.search(r'<[｜|]DSML[｜|](?:tool_calls|invoke)\b',r['content']):
        flag('tool_markup_in_content','正文出现序列化工具调用标记，需确认是否完成题目；工具没有执行','error')
    if r.get('tool_calls') and not t['payload'].get('tools'):
        flag('unexpected_tool_call','请求未提供工具，响应却包含 tool_calls；工具没有执行','error')
    mode=t['payload'].get('thinking')
    if isinstance(mode,dict) and mode.get('type')=='disabled' and r['reasoning']:
        flag('disabled_has_reasoning','请求关闭思考但仍返回 reasoning；参数可能被忽略或冲突')
    if r['http']==200 and r['format']=='json' and t['payload'].get('stream'):flag('stream_ignored','请求流式却返回 JSON；首字不可测')
    if r['http']==200 and r['format']=='sse' and not r['done']:flag('missing_done','没有 [DONE]；需结合 finish 与连接状态判断兼容性')
    if r['http']==200 and r['malformed_frames']:flag('malformed_stream',str(r['malformed_frames'])+' 个非预期数据帧','error')
    if r['tail_s'] is not None and r['tail_s']>5:flag('tail_wait',f'正文结束后又等待 {r["tail_s"]:.2f}s')
    if len(r['reasoning'])>8000 and len(r['content'])<500:flag('long_reasoning_short_answer','长思考配短答案，需阅读内容确认是否反复自检；不是自动判定乱思考')
    chunks=[r['reasoning'][i:i+120] for i in range(0,len(r['reasoning'])-119,120)]
    repetition=1-len(set(chunks))/len(chunks) if chunks else 0
    if len(chunks)>10 and repetition>.25:flag('repetitive_reasoning',f'120字符块重复占比 {repetition:.0%}；启发式线索')
    if r['metrics']['cache_status']=='conflict_or_invalid':flag('cache_fields_conflict','缓存计数字段冲突、越界或分母语义不明')
    u=r.get('usage') or {}
    p,h,m=(u.get(k) for k in ('prompt_tokens','prompt_cache_hit_tokens','prompt_cache_miss_tokens'))
    if all(isinstance(v,int) and not isinstance(v,bool) and v>=0 for v in (p,h,m)) and p!=h+m:
        flag('cache_accounting_mismatch',f'prompt_tokens 与 cache_hit + cache_miss 相差 {p-h-m} token；保留自报比例，口径差异不等于伪造')
    if len(r['returned_models'])>1:flag('multiple_response_labels','同一请求内出现多个返回模型名称')
    if r['returned_models'] and r['requested_model'] not in r['returned_models']:flag('alias_changed','请求模型与返回名称不同；名称不能证明真实模型身份')
    if r['http']==200 and r['usage'] is None:flag('missing_usage','未返回 usage，缓存和 token 速度未知')
    if r['first_content_s'] is not None and r['last_content_s']-r['first_content_s']<.3 and len(r['content'])>500:
        flag('buffered_burst','正文集中在不足0.3秒内交付，不能当作真实生成TPS')
    r['grade']=grade(r['content'],t)
    if r['grade']['correct'] and r['grade']['format_ok'] is False:
        flag('format_noncompliant','语义答案正确，但没有遵守要求的输出格式','info')
    r['flags']=flags
    r['delivered_correct']=r['complete'] and r['grade']['correct'] is True and r['content']!=r['reasoning']
    return r
