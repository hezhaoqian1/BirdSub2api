import json
import re
import threading
import time
import sys
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))

class MockHandler(BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*args):pass
    def json_reply(self,status,data):
        body=json.dumps(data).encode();self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.end_headers()
        try:self.wfile.write(body)
        except (BrokenPipeError,ConnectionResetError,ConnectionAbortedError):pass
    def do_GET(self):self.json_reply(200,{'data':[{'id':'mock-pro'},{'id':'mock-flash'}]})
    def do_POST(self):
        payload=json.loads(self.rfile.read(int(self.headers['Content-Length'])));self.server.seen.append(payload)
        model=payload.get('model','');prompt=payload['messages'][-1].get('content','')
        if model=='slow':time.sleep(3);self.json_reply(200,{});return
        if model=='error':self.json_reply(429,{'error':{'message':'echo '+self.headers.get('Authorization','')}});return
        if model=='redirect':self.send_response(307);self.send_header('Location','/v1/elsewhere');self.send_header('Content-Length','0');self.end_headers();return
        content='42';reason='短思考';finish='stop'
        if model.startswith('mock-'):
            if '0到127' in prompt:content=json.dumps(list(range(128)))
            elif '2026年10月16日' in prompt:content='2026-10-17'
            elif 'quantity=' in prompt:
                pairs=dict((int(a),int(b)) for a,b in re.findall(r'item=(\d+); quantity=(\d+)',prompt))
                ids=re.findall(r'item (\d+)',prompt);content=str(sum(pairs[int(i)] for i in ids))
            else:
                from suites import quality
                qs=quality(20261016)+json.loads((Path(__file__).resolve().parents[1]/'data/community.json').read_text(encoding='utf-8'))['questions']
                for q in qs:
                    if prompt==q['prompt']:
                        content=('\\boxed{'+str(q['expected'])+'}') if q['kind']=='boxed' else 'FINAL: '+str(q['expected']);break
            reason='可验证的简短推理。' if payload.get('thinking',{}).get('type')=='enabled' else ''
        if model=='copy':content=reason='I think 42.'
        if model=='length':content='';reason='loop '*40;finish='length'
        usage={'prompt_tokens':100,'completion_tokens':20,'prompt_tokens_details':{'cached_tokens':80}}
        if model=='missing':usage={'prompt_tokens':100,'completion_tokens':20}
        if model=='conflict':usage['prompt_cache_hit_tokens']=20
        if model=='json' or payload.get('stream') is False:
            self.json_reply(200,{'model':model,'choices':[{'message':{'content':content,'reasoning_content':reason},'finish_reason':finish}],'usage':usage});return
        self.send_response(200);self.send_header('Content-Type','text/event-stream');self.send_header('Connection','close');self.end_headers()
        self.close_connection=True
        def send(d):
            self.wfile.write(('data: '+json.dumps(d,ensure_ascii=False)+'\n\n').encode());self.wfile.flush()
        try:
            self.wfile.write(b': heartbeat\n\n');self.wfile.flush()
            if model=='manyframes':
                for i in range(4100):send({'choices':[{'delta':{'reasoning_content':'x'}}],'sequence':i})
            send({'model':model,'choices':[{'index':0,'delta':{'reasoning_content':reason}}]})
            send({'model':model,'choices':[{'index':0,'delta':{'content':content[:2]}}]})
            time.sleep(.015)
            send({'choices':[{'index':0,'delta':{'content':content[2:]}}]})
            if model=='malformed':self.wfile.write(b'data: not-json\n\n')
            if model=='badshape':send({'choices':[{'delta':'not-an-object'}]})
            if model=='multiline':self.wfile.write(b'data: {"choices": [],\ndata: "usage": {"prompt_tokens": 1}}\n\n')
            send({'choices':[{'index':0,'delta':{},'finish_reason':finish}],'usage':usage})
            send({'choices':[],'usage':{}})
            if model=='manyframes':send({'choices':[],'debug':self.headers.get('Authorization','')})
            if model!='nodone':self.wfile.write(b'data: [DONE]\n\n');self.wfile.flush()
        except (OSError,ConnectionError):pass

def start_mock():
    server=ThreadingHTTPServer(('127.0.0.1',0),MockHandler);server.seen=[]
    threading.Thread(target=server.serve_forever,daemon=True).start()
    return server,f'http://127.0.0.1:{server.server_port}'

if __name__=='__main__':
    server,url=start_mock();print(url,flush=True)
    try:
        while True:time.sleep(1)
    except KeyboardInterrupt:server.shutdown()
