"""Re-score stored evidence without sending API requests; preserve original report."""
import argparse,json,re
import engine
from suites import flag_result

def regrade(ident):
    if not re.fullmatch(r'[0-9a-f-]+',ident):raise ValueError('Invalid report ID')
    directory=engine.RESULTS/ident;original=directory/'report.original.json';current=directory/'report.json'
    data=json.loads((original if original.exists() else current).read_text(encoding='utf-8'))
    current_data=json.loads(current.read_text(encoding='utf-8'))
    reviews={r['id']:r['manual_review'] for r in current_data.get('rows',[]) if r.get('manual_review')}
    if data['status']=='running':raise ValueError('Cannot re-score an active run')
    if not original.exists():original.write_bytes(current.read_bytes())
    changes=[]
    for row in data['rows']:
        prior=dict(row['grade']);previous=row['delivered_correct']
        suite=row['suite']
        kind=('number' if suite=='cache' else 'json' if suite=='speed' else 'final' if suite=='quality' else
              ('boxed' if row.get('category')=='MATH-500' else 'final') if suite=='community' else 'exact')
        row['original_grade']=prior;row['assessment_version']='1.1.0'
        flag_result(row,dict(payload=row['request'],expected=row['expected'],kind=kind))
        if row['id'] in reviews:
            review=reviews[row['id']];row['manual_review']=review;row['grade_before_review']=dict(row['grade'])
            if review['verdict']!='reset':
                row['grade'].update(correct=review['verdict']=='correct',status='manual_pass' if review['verdict']=='correct' else 'manual_mismatch')
            row['delivered_correct']=bool(row['complete'] and row['grade']['correct'] and row['content']!=row['reasoning'])
            row['flags']=[f for f in row['flags'] if f['code']!='format_noncompliant']
            if row['grade']['correct'] and row['grade']['format_ok'] is False:
                row['flags'].append(dict(code='format_noncompliant',detail='语义答案经复核正确，但输出格式不符',severity='info'))
        if prior!=row['grade'] or previous!=row['delivered_correct']:
            changes.append(dict(id=row['id'],task=row['task'],before=prior,after=row['grade']))
    data['config']['assessment_version']='1.1.0'
    data['notices'].append('使用 v1.1 评分规则重新核对原始响应；没有重发请求。正确加法等式视为语义正确、格式不符。修改前报告保留为 report.original.json。')
    engine.save(data)
    (directory/'grading-changes.json').write_text(json.dumps(changes,ensure_ascii=False,indent=2),encoding='utf-8')
    print(json.dumps({'id':ident,'changes':len(changes)},ensure_ascii=False))

if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('run_ids',nargs='+');args=parser.parse_args()
    for ident in args.run_ids:regrade(ident)
