import json
import pathlib
import re
import sys
import threading
import tempfile
import unittest
from http.server import ThreadingHTTPServer

import requests
sys.path.insert(0,str(pathlib.Path(__file__).resolve().parents[1]))
import app


class HttpTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server=ThreadingHTTPServer(('127.0.0.1',0),app.Handler)
        cls.url=f'http://127.0.0.1:{cls.server.server_port}'
        threading.Thread(target=cls.server.serve_forever,daemon=True).start()
    @classmethod
    def tearDownClass(cls):cls.server.shutdown();cls.server.server_close()
    def test_home_and_token(self):
        r=requests.get(self.url,timeout=5);self.assertEqual(r.status_code,200)
        self.assertIn('DeepSeek Lab',r.text);self.assertIn('name="lab-token"',r.text)
        self.assertEqual(requests.get(self.url+'/api/meta',timeout=5).status_code,403)
        self.assertEqual(requests.get(self.url+'/api/meta',headers={'X-Lab-Token':app.TOKEN},timeout=5).status_code,200)
    def test_origin_and_host_protection(self):
        self.assertEqual(requests.get(self.url,headers={'Host':'evil.test'},timeout=5).status_code,403)
        self.assertEqual(requests.post(self.url+'/api/preview',json={},headers={'X-Lab-Token':app.TOKEN,'Origin':'https://evil.test'},timeout=5).status_code,403)
    def test_invalid_plan_and_path(self):
        headers={'X-Lab-Token':app.TOKEN}
        r=requests.post(self.url+'/api/preview',json={'targets':[]},headers=headers,timeout=5)
        self.assertEqual(r.status_code,400)
        r=requests.get(self.url+'/api/jobs/not-a-valid-id',headers=headers,timeout=5)
        self.assertEqual(r.status_code,400)
    def test_valid_preview_no_key_echo(self):
        r=requests.post(self.url+'/api/preview',headers={'X-Lab-Token':app.TOKEN},json={'targets':[{'url':'https://example.com','key':'secret-sentinel','model':'pro'}],'suites':['cache']},timeout=5)
        self.assertEqual(r.status_code,200);self.assertEqual(r.json()['calls'],12);self.assertNotIn('secret-sentinel',r.text)
    def test_lazy_report_and_case(self):
        old=app.engine.RESULTS
        with tempfile.TemporaryDirectory() as tmp:
            app.engine.RESULTS=pathlib.Path(tmp)
            try:
                directory=pathlib.Path(tmp)/'20261017-abcd';(directory/'cases').mkdir(parents=True)
                (directory/'view.json').write_text('{"live_rows": [{"id":"0-1"}]}',encoding='utf-8')
                (directory/'cases/0-1.json').write_text('{"content":"42"}',encoding='utf-8')
                headers={'X-Lab-Token':app.TOKEN}
                view=requests.get(self.url+'/api/jobs/20261017-abcd/view',headers=headers,timeout=5)
                self.assertEqual(view.json()['live_rows'][0]['id'],'0-1')
                case=requests.get(self.url+'/api/jobs/20261017-abcd/cases/0-1',headers=headers,timeout=5)
                self.assertEqual(case.json()['content'],'42')
            finally:app.engine.RESULTS=old

if __name__=='__main__':unittest.main()
