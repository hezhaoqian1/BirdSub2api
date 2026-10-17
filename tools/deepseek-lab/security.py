"""Private hosted deployment. No upstream API keys are stored here."""
import hashlib
import os
import secrets
import threading
import time
from http.cookies import SimpleCookie
from urllib.parse import urlsplit

HOSTED = os.environ.get('LAB_HOSTED') == '1' or bool(os.environ.get('RAILWAY_ENVIRONMENT_ID'))
PASSWORD_HASH = os.environ.get('LAB_PASSWORD_SHA256', '')
PUBLIC_URL = os.environ.get('LAB_PUBLIC_URL', '').rstrip('/')
if not PUBLIC_URL and os.environ.get('RAILWAY_PUBLIC_DOMAIN'):
    PUBLIC_URL = 'https://' + os.environ['RAILWAY_PUBLIC_DOMAIN']
SESSIONS = {}
ATTEMPTS = []
LOCK = threading.Lock()
COOKIE = '__Host-lab_session'


def validate_config():
    if HOSTED:
        if len(PASSWORD_HASH) != 64 or any(c not in '0123456789abcdef' for c in PASSWORD_HASH):
            raise RuntimeError('Hosted mode requires LAB_PASSWORD_SHA256')
        p = urlsplit(PUBLIC_URL)
        if p.scheme != 'https' or not p.hostname or p.path or p.query or p.fragment or p.username:
            raise RuntimeError('Hosted mode requires an HTTPS LAB_PUBLIC_URL or RAILWAY_PUBLIC_DOMAIN')
        if not os.environ.get('LAB_RESULTS_DIR'):
            raise RuntimeError('Hosted mode requires persistent LAB_RESULTS_DIR')


def authenticated(cookie):
    try:
        jar = SimpleCookie(); jar.load(cookie or '')
        token = jar[COOKIE].value
    except (KeyError, ValueError):
        return False
    with LOCK:
        return SESSIONS.get(token, 0) > time.time()


def login(password):
    now = time.time()
    with LOCK:
        ATTEMPTS[:] = [t for t in ATTEMPTS if now - t < 60]
        if len(ATTEMPTS) >= 20:
            return None, 429
        if not secrets.compare_digest(hashlib.sha256(password.encode()).hexdigest(), PASSWORD_HASH):
            ATTEMPTS.append(now)
            return None, 401
        for key in list(SESSIONS):
            if SESSIONS[key] <= now:
                del SESSIONS[key]
        if len(SESSIONS) >= 128:
            del SESSIONS[next(iter(SESSIONS))]
        token = secrets.token_urlsafe(32)
        SESSIONS[token] = now + 12 * 3600
    return token, 200


def logout(cookie):
    try:
        jar = SimpleCookie(); jar.load(cookie or '')
        with LOCK: SESSIONS.pop(jar[COOKIE].value, None)
    except (KeyError, ValueError):
        pass


def login_page(csrf, failed=False):
    return '''<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>登录 · DeepSeek Lab</title><style>body{background:#f6f7f9;color:#15283b;font:16px system-ui;margin:0;display:grid;min-height:100vh;place-items:center}main{background:white;border:1px solid #dde4ea;border-radius:20px;padding:36px;max-width:420px;margin:20px}h1{font-size:28px}p{line-height:1.7;color:#536477}input,button{box-sizing:border-box;width:100%;padding:14px;margin:10px 0;border:1px solid #bbc9d5;border-radius:8px;font:inherit}button{background:#1a5e62;color:white;cursor:pointer}.error{color:#b42318}</style>
<main><small>DS / LAB · 私有测试台</small><h1>登录后查看与测试</h1><p>测试历史包含完整请求和回答，仅向持有访问密码的人开放。渠道 API Key 不保存在历史中。</p>''' + ('<p class="error">登录失败或尝试过多，请稍后重试。</p>' if failed else '') + '''<form method="post" action="/login"><input type="hidden" name="csrf" value="''' + csrf + '''"><label for="password">访问密码</label><input id="password" type="password" name="password" autocomplete="current-password" required maxlength="256"><button>进入测试台</button></form><p id="loginError" class="error" role="alert"></p><p>会话有效期 12 小时；重启服务后需重新登录。</p></main><script src="/login.js"></script></html>'''
