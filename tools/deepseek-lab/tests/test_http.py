import json
import pathlib
import re
import sys
import threading
import tempfile
import unittest
import subprocess
import shutil
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
    def test_browser_entry_boot_preview_and_run(self):
        # Execute exactly the scripts the HTTP page loads, not a manually repaired order.
        home=requests.get(self.url,timeout=5).text
        scripts=re.findall(r'<script src="([^"]+)"',home)
        self.assertEqual(scripts,['/app.js'])
        source='\n;\n'.join(requests.get(self.url+path,timeout=5).text for path in scripts)
        harness=r"""
const vm=require('node:vm'),assert=require('node:assert/strict');
const fs=require('node:fs');
class Element {
  constructor(){this.children=[];this.innerHTML='';this.textContent='';this.value='';this.dataset={};this.content='csrf';}
  append(...x){this.children.push(...x)}
  querySelector(q){return this.nodes?.[q] || new Element()}
  querySelectorAll(){return []}
  replaceChildren(){this.children=[]}
  addEventListener(){}
  setAttribute(){}
}
const elements=new Map(),get=id=>{if(!elements.has(id))elements.set(id,new Element());return elements.get(id)};
for(const [id,value] of Object.entries({maxTokens:8192,repeats:1,concurrency:1,seed:20261016,idle:90,deadline:240,cacheDelay:3,thinking:'enabled',effort:'high'}))get(id).value=value;
const input=value=>Object.assign(new Element(),{value});
const target=new Element();target.nodes={'.name':input('test'),'.url':input('https://example.com'),'.key':input('sentinel')};
target.querySelectorAll=()=>[input('pro')];
const document={getElementById:get,createElement:()=>new Element(),createTextNode:x=>x,
 querySelector:q=>q.startsWith('meta')?{content:'csrf'}:null,
 querySelectorAll:q=>q==='.target'?[target]:q==='.suite input:checked'?[input('speed')]:[]};
const calls=[];
const fetch=async(url,options)=>{calls.push(url);let data;
 if(url==='/api/meta')data={version:'1.2.2',suites:{speed:['Speed <unsafe>','description']}};
 else if(url==='/api/history')data=[{id:'abc',started_at:'now',status:'complete',targets:[{name:'<img>',model:'pro'}],completed:1,planned:1}];
 else if(url==='/api/preview')data={calls:3,per_target:3,completion_token_budget:24576};
 else if(url==='/api/run')data={id:'abc'};
 else if(url==='/api/jobs/abc/status')data={status:'running',planned:3,completed:0,active:{},config:{suites:['speed']},targets:[],summary:[],rows:[]};
 else throw Error('Unexpected request '+url);
 return {ok:true,json:async()=>data};};
const context=vm.createContext({document,window:{addEventListener(){}},location:{hash:''},URLSearchParams,fetch,setTimeout:()=>0,clearTimeout(){},console});
const flush=async()=>{for(let i=0;i<8;i++)await new Promise(setImmediate)};
(async()=>{
 vm.runInContext(fs.readFileSync(0,'utf8'),context);await flush();
 assert.equal(get('error').textContent,'');assert.equal(get('targets').children.length,1);
 assert.match(get('suites').children[0].innerHTML,/&lt;unsafe&gt;/);
 assert.match(get('history').innerHTML,/&lt;img&gt;/);
 await get('preview').onclick();assert.match(get('estimate').textContent,/3/);
 await get('run').onclick();await flush();
 assert.ok(calls.includes('/api/run'));assert.ok(calls.includes('/api/jobs/abc/status'));
 assert.equal(get('error').textContent,'');
 console.log('bootstrap, escaped history, preview and run passed');
})().catch(e=>{console.error(e);process.exitCode=1});
"""
        self.assertIsNotNone(shutil.which('node'),'Node is required for frontend regression tests')
        result=subprocess.run(['node','-e',harness],input=source,text=True,encoding='utf-8',capture_output=True,timeout=15)
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)

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
