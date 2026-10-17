import hashlib
import pathlib
import re
import sys
import threading
import unittest
from http.server import ThreadingHTTPServer
from unittest.mock import patch

import requests
sys.path.insert(0,str(pathlib.Path(__file__).resolve().parents[1]))
import app
import security
from egress import public_socket


class HostedTests(unittest.TestCase):
    def setUp(self):
        self.config=patch.multiple(security,HOSTED=True,PUBLIC_URL='https://lab.example.com',PASSWORD_HASH=hashlib.sha256(b'test-password-only').hexdigest())
        self.config.start()
        security.SESSIONS.clear();security.ATTEMPTS.clear()
        self.server=ThreadingHTTPServer(('127.0.0.1',0),app.Handler)
        threading.Thread(target=self.server.serve_forever,daemon=True).start()
        self.url=f'http://127.0.0.1:{self.server.server_port}'
        self.headers={'Host':'lab.example.com','Origin':'https://lab.example.com'}
    def tearDown(self):
        self.server.shutdown();self.server.server_close();self.config.stop()
        security.SESSIONS.clear();security.ATTEMPTS.clear()
    def login(self):
        r=requests.post(self.url+'/login',headers=self.headers,data={'csrf':app.TOKEN,'password':'test-password-only'},allow_redirects=False,timeout=5)
        self.assertEqual(r.status_code,303)
        cookie=r.headers['Set-Cookie']
        for flag in ('Secure','HttpOnly','SameSite=Strict','Path=/'):self.assertIn(flag,cookie)
        return {**self.headers,'Cookie':cookie.split(';')[0],'X-Lab-Token':app.TOKEN}
    def test_all_evidence_and_actions_require_login(self):
        for path in ('/api/history','/api/jobs/20261017-abcd','/api/jobs/20261017-abcd/cases/0-1','/api/jobs/20261017-abcd/report.html','/api/guide'):
            r=requests.get(self.url+path,headers={**self.headers,'X-Lab-Token':app.TOKEN},timeout=5)
            self.assertEqual(r.status_code,401,path)
        for path in ('/api/models','/api/run','/api/review'):
            self.assertEqual(requests.post(self.url+path,headers={**self.headers,'X-Lab-Token':app.TOKEN},json={},timeout=5).status_code,401)
        self.assertEqual(requests.get(self.url,headers=self.headers,allow_redirects=False,timeout=5).status_code,303)
        self.assertEqual(requests.get(self.url+'/healthz',timeout=5).json(),{'ok':True})
    def test_login_csrf_logout_and_session(self):
        h=self.login()
        page=requests.get(self.url,headers=h,timeout=5)
        self.assertEqual(page.status_code,200);self.assertIn('logoutBtn',page.text)
        self.assertNotIn('__MODE__',page.text)
        self.assertEqual(requests.get(self.url+'/api/meta',headers=h,timeout=5).status_code,200)
        self.assertEqual(requests.post(self.url+'/api/preview',headers={**h,'Origin':'https://evil.test'},json={},timeout=5).status_code,403)
        no_csrf={k:v for k,v in h.items() if k!='X-Lab-Token'}
        self.assertEqual(requests.get(self.url+'/api/history',headers=no_csrf,timeout=5).status_code,403)
        self.assertEqual(requests.post(self.url+'/api/logout',headers=h,json={},timeout=5).status_code,200)
        self.assertEqual(requests.get(self.url+'/api/meta',headers=h,timeout=5).status_code,401)
    def test_bad_login_rate_limit(self):
        for i in range(20):self.assertEqual(security.login('wrong')[1],401)
        self.assertEqual(security.login('wrong')[1],429)
        self.assertEqual(requests.post(self.url+'/login',headers=self.headers,data={'password':'test-password-only','csrf':'wrong'},timeout=5).status_code,400)
    def test_fails_closed_without_password(self):
        with patch.object(security,'PASSWORD_HASH',''):
            with self.assertRaises(RuntimeError):security.validate_config()
    def test_private_dns_blocked_and_public_ip_pinned(self):
        conn=type('C',(),{'host':'example.com','port':443,'timeout':5,'source_address':None,'socket_options':[]})()
        for ip in ('127.0.0.1','169.254.169.254','10.0.0.1','::1','::ffff:127.0.0.1'):
            with patch('egress.socket.getaddrinfo',return_value=[(2,1,6,'',(ip,443))]),patch('egress.create_connection') as connect:
                with self.assertRaises(ValueError):public_socket(conn)
                connect.assert_not_called()
        with patch('egress.socket.getaddrinfo',return_value=[(2,1,6,'',('1.1.1.1',443))]),patch('egress.create_connection') as connect:
            public_socket(conn);self.assertEqual(connect.call_args.args[0],('1.1.1.1',443))


if __name__=='__main__':unittest.main()
