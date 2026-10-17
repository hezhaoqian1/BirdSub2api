import json
import pathlib
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

sys.path.insert(0,str(pathlib.Path(__file__).resolve().parents[1]))
import engine
from suites import build_plan,flag_result,grade,quality
from transport import collect,endpoints,usage_summary
from mock_upstream import start_mock


class PureTests(unittest.TestCase):
    def test_endpoint_normalization(self):
        for url in ['https://x.test','https://x.test/v1/','https://x.test/v1/chat/completions','https://x.test/dashboard']:
            self.assertEqual(endpoints(url)[1],'https://x.test/v1/chat/completions')
        self.assertEqual(endpoints('http://x.test/api/v1')[0],'http://x.test/api/v1/models')
        for url in ['file:///secret','https://key@x.test','https://x.test?key=abc']:
            with self.assertRaises(ValueError):endpoints(url)
    def test_missing_zero_and_conflicting_cache(self):
        self.assertIsNone(usage_summary({'prompt_tokens':100})['cache_percent'])
        self.assertEqual(usage_summary({'prompt_tokens':100,'prompt_cache_hit_tokens':0})['cache_percent'],0)
        self.assertEqual(usage_summary({'prompt_tokens':100,'prompt_cache_hit_tokens':90})['cache_percent'],90)
        self.assertIsNone(usage_summary({'prompt_tokens':100,'prompt_cache_hit_tokens':90,'cached_tokens':20})['cache_percent'])
        self.assertIsNone(usage_summary({'prompt_tokens':100,'prompt_cache_hit_tokens':101})['cache_percent'])
    def test_usage_schema(self):
        self.assertEqual(usage_summary({'input_tokens':10,'cache_read_input_tokens':80,'cache_creation_input_tokens':10})['cache_percent'],80)
        self.assertIsNone(usage_summary({'input_tokens':100,'cached_tokens':80})['cache_percent'])
        self.assertEqual(usage_summary({'prompt_cache_hit_tokens':80,'prompt_cache_miss_tokens':20})['cache_percent'],80)
        self.assertIsNone(usage_summary({'prompt_tokens':float('inf'),'prompt_cache_hit_tokens':80})['cache_percent'])
    def test_grading_semantics_and_format(self):
        r=grade('FINAL: C',{'kind':'final','expected':'(C)'})
        self.assertTrue(r['correct']);self.assertFalse(r['format_ok'])
        self.assertTrue(grade('Answer: \\boxed{0.5}',{'kind':'boxed','expected':'\\frac{1}{2}'})['correct'])
        self.assertEqual(grade('\\boxed{x+x}',{'kind':'boxed','expected':'2x'})['status'],'review')
        self.assertFalse(grade('FINAL: 5',{'kind':'final','expected':'50'})['correct'])
        self.assertFalse(grade('```json\n[1,2]\n```',{'kind':'json','expected':[1,2]})['format_ok'])
    def test_numeric_equation_is_format_failure_not_wrong_answer(self):
        r=grade('8009 + 3496 = 11505',{'kind':'number','expected':'11505'})
        self.assertTrue(r['correct']);self.assertFalse(r['format_ok'])
        self.assertFalse(grade('8009 + 3496 = 11605',{'kind':'number','expected':'11505'})['correct'])
        self.assertFalse(grade('The answer is not 11505',{'kind':'number','expected':'11505'})['correct'])
        self.assertTrue(grade('**11505**',{'kind':'number','expected':'11505'})['correct'])
        self.assertTrue(grade('8009 + 3496 = **11505**',{'kind':'number','expected':'11505'})['correct'])
    def test_plan_count_and_controls(self):
        cfg={'suites':['speed','quality','reasoning','cache','community']};a=build_plan(cfg,'nonce1');b=build_plan(cfg,'nonce2')
        self.assertEqual(len(a),51)
        c=[t for t in a if t['suite']=='cache'];self.assertEqual(c[0]['payload'],c[1]['payload'])
        self.assertNotEqual(c[0]['expected'],c[3]['expected'])
        self.assertNotEqual(c[0]['payload'],[t for t in b if t['suite']=='cache'][0]['payload'])
        off=build_plan({'suites':['quality'],'thinking':'disabled'},'x');self.assertNotIn('reasoning_effort',off[0]['payload'])
    def test_custom_body_preserved(self):
        body={'messages':[{'role':'user','content':'x'}],'stream':False,'thinking':{'type':'disabled'},'reasoning_effort':'max','max_tokens':1500}
        p=build_plan({'suites':['custom'],'custom_body':body},'x')
        self.assertEqual(p[0]['payload'],body)
    def test_answer_reproducibility(self):
        self.assertEqual(quality(123),quality(123));self.assertNotEqual(quality(123)[0:6],quality(124)[0:6])
        self.assertEqual(quality(123)[-1]['expected'],'B')


class WireTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):cls.server,cls.base=start_mock()
    @classmethod
    def tearDownClass(cls):cls.server.shutdown();cls.server.server_close()
    def call(self,model='normal',**kwargs):
        p={'model':model,'messages':[{'role':'user','content':'42?'}],'stream':True,'thinking':{'type':'disabled'}}
        r=collect(self.base+'/v1/chat/completions','secret-test-key',p,**kwargs)
        r.update(requested_model=model)
        return flag_result(r,{'payload':p,'expected':'42','kind':'exact'})
    def test_sse_empty_usage_and_timing(self):
        r=self.call();self.assertTrue(r['complete']);self.assertEqual(r['content'],'42');self.assertTrue(r['done'])
        self.assertEqual(r['metrics']['cache_percent'],80);self.assertEqual(len(r['usage_frames']),1)
        self.assertLessEqual(r['first_reasoning_s'],r['first_content_s']);self.assertGreaterEqual(r['last_content_s'],r['first_content_s'])
        self.assertIn('disabled_has_reasoning',[f['code'] for f in r['flags']])
        r['usage'].update(prompt_tokens=100,prompt_cache_hit_tokens=80,prompt_cache_miss_tokens=19)
        flag_result(r,{'payload':{},'expected':'42','kind':'exact'})
        self.assertIn('cache_accounting_mismatch',{f['code'] for f in r['flags']})
    def test_json_not_ttft(self):
        r=self.call('json');self.assertTrue(r['complete']);self.assertIsNone(r['first_content_s']);self.assertIn('stream_ignored',[f['code'] for f in r['flags']])
    def test_long_stream_keeps_bounded_redacted_tail(self):
        r=self.call('manyframes')
        self.assertTrue(r['complete']);self.assertEqual(r['content'],'42')
        self.assertEqual(r['event_count'],4106);self.assertEqual(len(r['events']),4000)
        self.assertTrue(r['events_truncated']);self.assertEqual(len(r['tail_events']),64)
        tail=r['tail_events']
        self.assertTrue(any(c.get('finish_reason')=='stop' for e in tail for c in e['data'].get('choices',[])))
        self.assertTrue(any(e['data'].get('usage',{}).get('prompt_tokens')==100 for e in tail))
        self.assertNotIn('secret-test-key',json.dumps(r));self.assertIn('[API_KEY_REDACTED]',json.dumps(tail))
    def test_truncation_and_copy(self):
        r=self.call('length');self.assertFalse(r['complete']);self.assertFalse(r['delivered_correct'])
        r=self.call('copy');self.assertIn('reasoning_copied_to_content',[f['code'] for f in r['flags']]);self.assertFalse(r['delivered_correct'])
    def test_unknown_cache(self):
        self.assertIsNone(self.call('missing')['metrics']['cache_percent']);self.assertIsNone(self.call('conflict')['metrics']['cache_percent'])
    def test_bad_frame_and_multiline(self):
        self.assertFalse(self.call('malformed')['complete']);self.assertTrue(self.call('multiline')['complete'])
        self.assertFalse(self.call('badshape')['complete'])
    def test_no_done_recorded(self):
        r=self.call('nodone');self.assertFalse(r['done']);self.assertIn('missing_done',[f['code'] for f in r['flags']])
    def test_redaction_and_no_redirect(self):
        r=self.call('error');self.assertEqual(r['http'],429);self.assertNotIn('secret-test-key',json.dumps(r))
        codes={f['code'] for f in r['flags']}
        self.assertIn('http_error',codes)
        self.assertFalse(codes & {'empty_answer','missing_usage','stream_ignored','malformed_stream'})
        n=len(self.server.seen);r=self.call('redirect');self.assertEqual(r['http'],307);self.assertEqual(len(self.server.seen),n+1)
    def test_stop_with_empty_final_marker(self):
        r=self.call();r['content']='FINAL: ';r['reasoning']='The result is 42.'
        flag_result(r,{'payload':{},'expected':'42','kind':'final'})
        self.assertIn('incomplete_final_marker',{f['code'] for f in r['flags']})
        self.assertFalse(r['delivered_correct'])
        r['content']='Answer is 42.\n\\boxed{'
        flag_result(r,{'payload':{},'expected':'42','kind':'boxed'})
        self.assertIn('incomplete_final_marker',{f['code'] for f in r['flags']})
        r['content']='<｜DSML｜tool_calls><｜DSML｜invoke name="exec_command">'
        flag_result(r,{'payload':{},'expected':'42','kind':'final'})
        self.assertIn('tool_markup_in_content',{f['code'] for f in r['flags']})
    def test_wall_deadline_and_cancel(self):
        t=time.perf_counter();r=self.call('slow',deadline=.7,idle=10);self.assertEqual(r['error'],'wall_deadline');self.assertLess(time.perf_counter()-t,2.5)
        stop=threading.Event();timer=threading.Timer(.4,stop.set);timer.start();r=self.call('slow',stop=stop,deadline=10);self.assertEqual(r['error'],'cancelled')
    def test_model_discovery(self):
        r=collect(self.base+'/v1/models','secret-test-key',method='GET');self.assertEqual(len(r['document']['data']),2)
    def test_later_worker_failure_is_reported_promptly(self):
        old=engine.RESULTS
        def broken(url,key,payload,stop,*args):
            if payload['model']=='slow':stop.wait(4)
            raise RuntimeError('synthetic worker failure')
        with tempfile.TemporaryDirectory() as tmp:
            engine.RESULTS=pathlib.Path(tmp)
            try:
                with mock.patch.object(engine,'collect',side_effect=broken):
                    start=time.monotonic()
                    d=engine.start({'targets':[{'url':self.base,'key':'sentinel','model':m,'name':m} for m in ('slow','broken')],'suites':['speed'],'concurrency':2})
                    j=engine.JOBS[d['id']]
                    while j['status']=='running' and time.monotonic()-start<3:time.sleep(.05)
                    self.assertEqual(j['status'],'failed')
                    self.assertLess(time.monotonic()-start,3)
                    self.assertIn('RuntimeError',' '.join(j['notices']))
            finally:engine.RESULTS=old
    def test_full_engine_and_exports(self):
        old=engine.RESULTS
        with tempfile.TemporaryDirectory() as tmp:
            engine.RESULTS=pathlib.Path(tmp)
            try:
                cfg={'targets':[{'url':self.base,'key':'secret-test-key','model':'mock-pro','name':'本地模拟'}],
                     'suites':['speed','quality','reasoning','cache','community'],'cache_delay':0}
                replace_original=pathlib.Path.replace;denied=[]
                def briefly_locked(path,target):
                    if pathlib.Path(target).name=='report.json' and not denied:
                        denied.append(True);raise PermissionError('synthetic Windows reader lock')
                    return replace_original(path,target)
                with mock.patch.object(pathlib.Path,'replace',briefly_locked):job=engine.start(cfg)
                self.assertTrue(denied)
                j=engine.JOBS[job['id']];limit=time.time()+100
                self.assertTrue((engine.RESULTS/job['id']/'report.json').exists())
                self.assertIn(job['id'],[h['id'] for h in engine.history()])
                while j['status']=='running' and time.time()<limit:time.sleep(.1)
                self.assertEqual(j['status'],'complete');self.assertEqual(j['completed'],51)
                # Wait for final export after state transition.
                report=engine.RESULTS/job['id']/'report.html'
                for _ in range(100):
                    if report.exists():break
                    time.sleep(.05)
                data=json.loads((report.parent/'report.json').read_text(encoding='utf-8'))
                self.assertEqual(data['summary'][0]['quality_correct'],32)
                self.assertEqual(data['summary'][0]['quality_semantic_correct'],32)
                self.assertEqual(data['summary'][0]['warm_cache_correct'],80)
                self.assertNotIn('secret-test-key',report.read_text(encoding='utf-8'))
                self.assertNotIn('secret-test-key',json.dumps(data))
                self.assertTrue(report.exists())
                view=json.loads((report.parent/'view.json').read_text(encoding='utf-8'))
                self.assertEqual(len(view['live_rows']),51)
                self.assertNotIn('rows',view)
                self.assertNotIn('request',view['live_rows'][0])
                self.assertEqual(json.loads((report.parent/'cases/0-1.json').read_text(encoding='utf-8'))['content'],data['rows'][0]['content'])
                reviewed=engine.review_case(job['id'],'0-4','incorrect','fixture: verify review audit')
                self.assertEqual(reviewed['summary'][0]['quality_correct'],31)
                import regrade
                regrade.regrade(job['id'])
                reassessed=json.loads((report.parent/'report.json').read_text(encoding='utf-8'))
                self.assertEqual(reassessed['summary'][0]['quality_correct'],31)
                self.assertEqual(next(r for r in reassessed['rows'] if r['id']=='0-4')['manual_review']['verdict'],'incorrect')
                reset=engine.review_case(job['id'],'0-4','reset','fixture: restore automatic grade')
                self.assertEqual(reset['summary'][0]['quality_correct'],32)
                self.assertEqual(len(json.loads((report.parent/'manual_reviews.json').read_text(encoding='utf-8'))),2)
                # Simulate a crash after incremental snapshots; all evidence
                # must survive recovery without reopening an upstream request.
                j['status']='running';engine.save(j)
                partial=json.loads((report.parent/'report.json').read_text(encoding='utf-8'))
                self.assertNotIn('rows',partial)
                self.assertEqual(len(partial['completed_case_ids']),51)
                import app
                app.recover_interrupted()
                recovered=json.loads((report.parent/'report.json').read_text(encoding='utf-8'))
                self.assertEqual(recovered['status'],'interrupted')
                self.assertEqual(len(recovered['rows']),51)
                j['status']='complete'
            finally:engine.RESULTS=old

if __name__=='__main__':unittest.main()
