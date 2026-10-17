"""Local-only UI for DeepSeek Lab. Run: python app.py --open"""
import argparse
import json
import os
import pathlib
import secrets
import threading
import webbrowser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit, parse_qs

import engine
import security
from suites import SUITES, VERSION
from transport import collect, endpoints

ROOT=pathlib.Path(__file__).parent
TOKEN=secrets.token_urlsafe(32)


class Handler(BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def send(self,status,body,kind='application/json; charset=utf-8',attachment=None,headers=None):
        if not isinstance(body,bytes):body=(json.dumps(body,ensure_ascii=False) if isinstance(body,(dict,list)) else body).encode('utf-8')
        self.send_response(status);self.send_header('Content-Type',kind);self.send_header('Content-Length',str(len(body)))
        self.send_header('Cache-Control','no-store');self.send_header('X-Content-Type-Options','nosniff')
        self.send_header('Cross-Origin-Resource-Policy','same-origin');self.send_header('X-Frame-Options','DENY')
        self.send_header('Referrer-Policy','no-referrer')
        if security.HOSTED:
            self.send_header('Strict-Transport-Security','max-age=31536000')
            if not attachment:self.send_header('Content-Security-Policy',"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
        for k,v in (headers or {}).items():self.send_header(k,v)
        if attachment:self.send_header('Content-Disposition','attachment; filename="'+attachment+'"')
        self.end_headers()
        try:self.wfile.write(body)
        except (BrokenPipeError,ConnectionResetError):pass
    def check(self,api=False):
        host=self.headers.get('Host','')
        allowed={f'127.0.0.1:{self.server.server_port}',f'localhost:{self.server.server_port}'}
        if security.HOSTED:allowed={urlsplit(security.PUBLIC_URL).netloc}
        if host not in allowed:self.send(403,{'error':'不允许的访问 Host'});return False
        origin=self.headers.get('Origin')
        origins={security.PUBLIC_URL} if security.HOSTED else {'http://'+x for x in allowed}
        if origin and origin not in origins:self.send(403,{'error':'不允许跨站调用'});return False
        if security.HOSTED and urlsplit(self.path).path not in ('/login','/login.js') and not security.authenticated(self.headers.get('Cookie')):
            if api:self.send(401,{'error':'请先登录，或会话已过期'})
            else:self.send(303,'',headers={'Location':'/login'})
            return False
        if api and not secrets.compare_digest(self.headers.get('X-Lab-Token',''),TOKEN):
            self.send(403,{'error':'页面会话已过期，请刷新页面'});return False
        return True
    def do_GET(self):
        path=urlsplit(self.path).path
        if path=='/healthz':self.send(200,{'ok':True});return
        if not self.check(path.startswith('/api/')):return
        try:
            if path=='/login' and security.HOSTED:
                self.send(200,security.login_page(TOKEN),'text/html; charset=utf-8')
            elif path=='/login.js' and security.HOSTED:
                self.send(200,(ROOT/'static/login.js').read_bytes(),'text/javascript; charset=utf-8')
            elif path=='/':
                page=(ROOT/'static/index.html').read_text(encoding='utf-8').replace('__LAB_TOKEN__',TOKEN)
                page=page.replace('__MODE__','登录保护 · 历史保存至持久卷' if security.HOSTED else '仅在本机运行')
                page=page.replace('__LOGOUT__','<button id="logoutBtn" class="ghost">退出登录</button>' if security.HOSTED else '')
                self.send(200,page,'text/html; charset=utf-8')
            elif path=='/app.js':
                # One response and scope: UI startup cannot outlive/miss its renderer dependency.
                bundle=(ROOT/'static/report.js').read_bytes()+b'\n;\n'+(ROOT/'static/app.js').read_bytes()
                self.send(200,bundle,'text/javascript; charset=utf-8')
            elif path in ('/style.css','/report.js'):
                p=ROOT/'static'/path[1:];self.send(200,p.read_bytes(),'text/css; charset=utf-8' if p.suffix=='.css' else 'text/javascript; charset=utf-8')
            elif path=='/api/meta':self.send(200,dict(version=VERSION,suites=SUITES))
            elif path=='/api/history':self.send(200,engine.history())
            elif path=='/api/guide':self.send(200,{'text':(ROOT/'README.md').read_text(encoding='utf-8')})
            elif path.startswith('/api/jobs/'):
                parts=path.split('/');ident=parts[3]
                if not ident or any(c not in '0123456789abcdef-' for c in ident):raise ValueError('无效报告编号')
                if len(parts)==6 and parts[4]=='cases':
                    if any(c not in '0123456789-' for c in parts[5]):raise ValueError('无效请求编号')
                    stored=engine.RESULTS/ident/'cases'/(parts[5]+'.json')
                    if stored.exists():self.send(200,stored.read_bytes());return
                    with engine.LOCK:job=engine.JOBS.get(ident)
                    data=engine.snapshot(job,True) if job else json.loads((engine.RESULTS/ident/'report.json').read_text(encoding='utf-8'))
                    row=next((r for r in data['rows'] if r['id']==parts[5]),None)
                    self.send(200,row) if row else self.send(404,{'error':'该请求尚未完成或不存在'})
                elif len(parts)==5 and parts[4]=='view':
                    stored=engine.RESULTS/ident/'view.json'
                    if stored.exists():self.send(200,stored.read_bytes());return
                    with engine.LOCK:job=engine.JOBS.get(ident)
                    data=engine.snapshot(job,True) if job else json.loads((engine.RESULTS/ident/'report.json').read_text(encoding='utf-8'))
                    data['live_rows']=[{k:r.get(k) for k in ('id','target_id','target_name','requested_model','task','suite','grade','delivered_correct','total_s','flags')} for r in data.pop('rows')]
                    self.send(200,data)
                elif len(parts)==5 and parts[4] in ('report.json','report.html'):
                    p=engine.RESULTS/ident/parts[4]
                    if not p.exists():self.send(404,{'error':'报告尚未生成'});return
                    self.send(200,p.read_bytes(),'text/html; charset=utf-8' if p.suffix=='.html' else 'application/json; charset=utf-8',ident+p.suffix)
                else:
                    with engine.LOCK:job=engine.JOBS.get(ident)
                    if job:self.send(200,engine.snapshot(job,not path.endswith('/status')))
                    else:self.send(200,json.loads((engine.RESULTS/ident/'report.json').read_text(encoding='utf-8')))
            else:self.send(404,{'error':'Not found'})
        except FileNotFoundError:self.send(404,{'error':'报告不存在'})
        except Exception as exc:self.send(400,{'error':str(exc) if isinstance(exc,ValueError) else type(exc).__name__})
    def do_POST(self):
        if security.HOSTED and urlsplit(self.path).path=='/login':
            if not self.check(False):return
            try:
                length=int(self.headers.get('Content-Length',0))
                if not 0<length<=2048 or 'application/x-www-form-urlencoded' not in self.headers.get('Content-Type',''):raise ValueError()
                data=parse_qs(self.rfile.read(length).decode())
                if not secrets.compare_digest(data.get('csrf',[''])[0],TOKEN):raise ValueError()
                token,status=security.login(data.get('password',[''])[0])
                if token:self.send(303,'',headers={'Location':'/','Set-Cookie':f'{security.COOKIE}={token}; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=43200'})
                else:self.send(status,security.login_page(TOKEN,True),'text/html; charset=utf-8')
            except (ValueError,UnicodeError):self.send(400,{'error':'无效登录请求'})
            return
        if not self.check(True):return
        try:
            if 'application/json' not in self.headers.get('Content-Type',''):raise ValueError('只接受 JSON')
            length=int(self.headers.get('Content-Length',0))
            if not 0<length<=3_000_000:raise ValueError('请求大小需在 1–3MB 内')
            data=json.loads(self.rfile.read(length));path=urlsplit(self.path).path
            if path=='/api/logout':
                security.logout(self.headers.get('Cookie'))
                self.send(200,{'ok':True},headers={'Set-Cookie':f'{security.COOKIE}=; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=0'})
            elif path=='/api/models':
                if not data.get('key'):raise ValueError('请输入 API Key')
                r=collect(endpoints(data['url'])[0],data['key'],idle=20,deadline=35,method='GET')
                doc=r.get('document') or {};models=doc.get('data',[])
                if r['http']!=200 or r['error']:self.send(400,{'error':f'模型列表获取失败 HTTP {r["http"]} / {r["error"] or "上游拒绝"}；可手动输入模型名'});return
                self.send(200,{'models':sorted({m['id'] for m in models if isinstance(m,dict) and isinstance(m.get('id'),str)})})
            elif path=='/api/preview':self.send(200,engine.validate(data))
            elif path=='/api/run':self.send(200,engine.start(data))
            elif path=='/api/stop':
                with engine.LOCK:job=engine.JOBS.get(data['id'])
                if job:job['stop'].set()
                self.send(200,{'stopping':bool(job)})
            elif path=='/api/review':
                self.send(200,engine.review_case(data['id'],data['case_id'],data['verdict'],data.get('note','')))
            else:self.send(404,{'error':'Not found'})
        except Exception as exc:self.send(400,{'error':str(exc) if isinstance(exc,(ValueError,KeyError)) else type(exc).__name__})


def recover_interrupted():
    # Recover interrupted snapshots without pretending unfinished requests succeeded.
    for d in engine.history():
        if d['status']=='running':
            p=engine.RESULTS/d['id']/'report.json';obj=json.loads(p.read_text(encoding='utf-8'));obj['status']='interrupted'
            if 'rows' not in obj:
                obj['rows']=[json.loads((p.parent/'cases'/(case_id+'.json')).read_text(encoding='utf-8')) for case_id in obj.get('completed_case_ids',[])]
            obj.setdefault('notices',[]).append('上次进程退出，未完成记录不计成功；重新测试会另建报告。')
            obj['active']={};engine.save(obj)


def serve(port=8767,open_browser=False):
    # Acquire the listening port before recovery; a duplicate launch must not
    # mark the running instance's reports interrupted.
    security.validate_config()
    server=ThreadingHTTPServer(('0.0.0.0' if security.HOSTED else '127.0.0.1',port),Handler)
    engine.RESULTS.mkdir(parents=True,exist_ok=True)
    recover_interrupted()
    url=f'http://127.0.0.1:{server.server_port}/';print('DeepSeek Lab '+VERSION+'  '+url,flush=True)
    if open_browser:webbrowser.open(url)
    try:server.serve_forever()
    except KeyboardInterrupt:pass
    finally:
        for job in engine.JOBS.values():job['stop'].set()
        server.server_close()


if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--port',type=int,default=int(os.environ.get('PORT','8767')));parser.add_argument('--open',action='store_true')
    args=parser.parse_args();serve(args.port,args.open)
