"""Bounded OpenAI-compatible collector. No retries or redirect credential forwarding."""
import hashlib
import json
import multiprocessing as mp
import math
import os
import queue
import time
from urllib.parse import urlsplit, urlunsplit

import requests

MAX_BYTES = 8 * 1024 * 1024


def endpoints(url):
    p = urlsplit(url.strip())
    if p.scheme not in ('http', 'https') or not p.hostname or p.username or p.password or p.query or p.fragment:
        raise ValueError('请输入不带账号、查询参数或片段的 http(s) API 地址')
    path = p.path.rstrip('/')
    if path.endswith('/chat/completions'):
        root = path[:-len('/chat/completions')]
    elif path.endswith('/models'):
        root = path[:-len('/models')]
    elif not path or path == '/dashboard':
        root = '/v1'
    else:
        root = path  # preserves /v1, /api/v1 and custom gateways
    base = urlunsplit((p.scheme, p.netloc, root, '', ''))
    return base + '/models', base + '/chat/completions'


def scrub(value, key):
    if isinstance(value, str):
        return value.replace(key, '[API_KEY_REDACTED]') if key else value
    if isinstance(value, list):
        return [scrub(v, key) for v in value]
    if isinstance(value, dict):
        return {str(scrub(k, key)): scrub(v, key) for k, v in value.items()}
    return value


def _wire(out, method, url, key, payload, idle):
    """Runs in a disposable process; parent enforces wall deadline/cancel even preheaders."""
    start = time.perf_counter()
    def emit(kind, data):
        out.put((kind, time.perf_counter() - start, data))
    try:
        with requests.Session() as session:
            session.trust_env = False  # do not load .netrc or implicit credential-bearing proxies
            if os.environ.get('LAB_HOSTED')=='1' or os.environ.get('RAILWAY_ENVIRONMENT_ID'):
                from egress import PublicOnlyAdapter
                session.mount('http://',PublicOnlyAdapter());session.mount('https://',PublicOnlyAdapter())
            with session.request(method, url, headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'},
                                 data=payload, stream=True, timeout=(min(12, idle), idle), allow_redirects=False) as response:
                emit('headers', {'http': response.status_code, 'headers': {k: v for k, v in response.headers.items()
                     if k.lower() in ('content-type', 'x-request-id', 'retry-after')}})
                is_sse = 'text/event-stream' in response.headers.get('content-type', '').lower()
                emit('format', 'sse' if is_sse else 'json')
                if is_sse and response.status_code == 200:
                    parts = []; size = 0
                    # chunk_size=1 prevents requests' 512-byte client buffering from inflating TTFT.
                    for raw in response.iter_lines(chunk_size=1):
                        size += len(raw)
                        if size > MAX_BYTES:
                            raise ValueError('response_size_limit')
                        if not raw:
                            if parts:
                                text = b'\n'.join(parts).decode('utf-8', errors='replace'); parts = []
                                if text.strip() == '[DONE]':
                                    emit('done', True); break
                                try:
                                    emit('data', json.loads(text))
                                except json.JSONDecodeError:
                                    emit('malformed', text[:1000])
                        elif raw.startswith(b'data:'):
                            parts.append(raw[5:].lstrip(b' '))
                    if parts:
                        text = b'\n'.join(parts).decode('utf-8', errors='replace')
                        if text.strip() == '[DONE]': emit('done', True)
                        else:
                            try: emit('data', json.loads(text))
                            except json.JSONDecodeError: emit('malformed', text[:1000])
                else:
                    body = bytearray()
                    for chunk in response.iter_content(8192):
                        body.extend(chunk)
                        if len(body) > MAX_BYTES: raise ValueError('response_size_limit')
                    try: emit('data', json.loads(body))
                    except (ValueError, UnicodeDecodeError): emit('non_json', body.decode('utf-8', errors='replace')[:2000])
    except Exception as exc:
        # requests can wrap a streaming urllib3 ReadTimeoutError in ConnectionError.
        # Classify without serializing exception strings (they may contain credentials).
        text = str(exc).lower()
        if isinstance(exc, requests.exceptions.ConnectTimeout): code='connect_timeout'
        elif isinstance(exc, requests.exceptions.ReadTimeout) or 'read timed out' in text: code='read_idle_timeout'
        elif isinstance(exc, ValueError) and str(exc)=='response_size_limit': code='response_size_limit'
        elif isinstance(exc, ValueError) and str(exc)=='hosted_private_address_blocked': code='hosted_private_address_blocked'
        else: code=type(exc).__name__
        emit('error', code)
    finally:
        emit('end', None)


def numeric(v):
    return v if isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v) and v >= 0 else None


def usage_summary(usage):
    """Direct API schemas only; ledger input_tokens excluding cache is a different contract."""
    u = usage or {}; candidates = {}
    for k in ('prompt_cache_hit_tokens', 'cache_read_input_tokens', 'cached_tokens'):
        if k in u: candidates[k] = u[k]
    for k in ('prompt_tokens_details', 'input_tokens_details'):
        if isinstance(u.get(k), dict) and 'cached_tokens' in u[k]:
            candidates[k + '.cached_tokens'] = u[k]['cached_tokens']
    vals = [numeric(v) for v in candidates.values()]
    known = bool(vals) and all(v is not None for v in vals) and len(set(vals)) == 1
    hit = vals[0] if known else None
    prompt = numeric(u.get('prompt_tokens'))
    schema = 'openai_prompt_total' if prompt is not None else 'unknown'
    if prompt is None and 'input_tokens' in u:
        prompt = numeric(u['input_tokens']); schema = 'input_tokens_semantics_unknown'
        # Anthropic-style input_tokens excludes cache, unlike OpenAI prompt_tokens.
        if 'cache_read_input_tokens' in u and prompt is not None:
            prompt += (numeric(u.get('cache_read_input_tokens')) or 0) + (numeric(u.get('cache_creation_input_tokens')) or 0)
            schema = 'anthropic_style_total'
    if prompt is None and numeric(u.get('prompt_cache_miss_tokens')) is not None and hit is not None:
        prompt = u['prompt_cache_miss_tokens'] + hit; schema = 'deepseek_hit_plus_miss'
    valid = known and prompt is not None and prompt > 0 and hit <= prompt and schema != 'input_tokens_semantics_unknown'
    completion = numeric(u.get('completion_tokens', u.get('output_tokens')))
    details = u.get('completion_tokens_details') or u.get('output_tokens_details') or {}
    reasoning = numeric(details.get('reasoning_tokens')) if isinstance(details, dict) else None
    return dict(prompt_tokens=prompt, completion_tokens=completion, reasoning_tokens=reasoning,
                cache_candidates=candidates, cache_tokens=hit, cache_percent=100*hit/prompt if valid else None,
                cache_status=('valid_reported' if valid else 'missing' if not candidates else 'conflict_or_invalid'), schema=schema)


def collect(url, key, payload=None, stop=None, idle=90, deadline=240, method='POST'):
    raw = json.dumps(payload, ensure_ascii=False, separators=(',', ':')).encode() if payload is not None else None
    result = dict(http=None, headers={}, format=None, content='', reasoning='', returned_models=[], usage_frames=[],
                  first_event_s=None, first_reasoning_s=None, first_content_s=None, last_content_s=None,
                  finish=None, done=False, error=None, events=[], tool_calls=[], malformed_frames=0,
                  request_hash=hashlib.sha256(raw).hexdigest() if raw else None, stream_requested=(payload or {}).get('stream', False))
    ctx = mp.get_context('spawn'); output = ctx.Queue(maxsize=256)
    process = ctx.Process(target=_wire, args=(output, method, url, key, raw, idle), daemon=True)
    start = time.perf_counter(); process.start(); finished = False
    try:
        while not finished:
            elapsed = time.perf_counter() - start
            if stop is not None and stop.is_set(): result['error'] = 'cancelled'; break
            if elapsed >= deadline: result['error'] = 'wall_deadline'; break
            try: kind, t, data = output.get(timeout=.1)
            except queue.Empty:
                if not process.is_alive(): result['error'] = result['error'] or 'worker_exited_without_end'; break
                continue
            result['total_s'] = t
            if kind == 'end': finished = True; continue
            if kind == 'headers': result.update(data); result['headers_s'] = t
            elif kind == 'format': result['format'] = data
            elif kind == 'error': result['error'] = data
            elif kind == 'done': result['done'] = True
            elif kind in ('malformed', 'non_json'):
                result['malformed_frames'] += 1; result['error_sample'] = data
            elif kind == 'data':
                if len(result['events']) < 4000: result['events'].append({'t': t, 'data': data})
                else: result['events_truncated'] = True
                if not isinstance(data, dict): result['malformed_frames'] += 1; continue
                if method == 'GET': result['document'] = data; continue
                if data.get('error'): result['response_error'] = data['error']
                if data.get('model') and data['model'] not in result['returned_models']: result['returned_models'].append(data['model'])
                if isinstance(data.get('usage'), dict) and data['usage']: result['usage_frames'].append(data['usage'])
                choices = data.get('choices') or []
                if not isinstance(choices, list):
                    result['malformed_frames'] += 1; continue
                for choice in choices:
                    if not isinstance(choice, dict):
                        result['malformed_frames'] += 1; continue
                    if choice.get('index', 0) != 0: continue
                    if choice.get('finish_reason'):
                        result['finish'] = choice['finish_reason']; result['finish_s'] = t
                    msg = choice.get('delta') or choice.get('message') or {}
                    if not isinstance(msg, dict):
                        result['malformed_frames'] += 1; continue
                    if msg.get('tool_calls'): result['tool_calls'].append(msg['tool_calls'])
                    for field, target in [('content', 'content'), ('reasoning_content', 'reasoning')]:
                        value = msg.get(field) or (msg.get('reasoning') if target == 'reasoning' else '') or ''
                        if isinstance(value, list): value = ''.join(x.get('text', '') for x in value if isinstance(x, dict))
                        if not isinstance(value, str) or not value: continue
                        result[target] += value
                        if result['format'] == 'sse':
                            if result['first_event_s'] is None: result['first_event_s'] = t
                            if result['first_' + target + '_s'] is None: result['first_' + target + '_s'] = t
                            if target == 'content': result['last_content_s'] = t
    finally:
        if process.is_alive():
            if finished: process.join(timeout=.5)
            if process.is_alive(): process.terminate(); process.join(timeout=2)
        output.close()
    result['wall_s'] = time.perf_counter() - start
    if not finished: result['total_s'] = result['wall_s']
    result['usage'] = result['usage_frames'][-1] if result['usage_frames'] else None
    result['metrics'] = usage_summary(result['usage'])
    result['complete'] = (result['http'] == 200 and bool(result['content'].strip()) and result['finish'] == 'stop'
                          and not result['error'] and not result.get('response_error') and not result['malformed_frames'])
    result['tail_s'] = max(0, result.get('total_s', 0) - result['last_content_s']) if result['last_content_s'] is not None else None
    result['reported_completion_tps'] = (result['metrics']['completion_tokens'] / result['total_s']
        if result['metrics']['completion_tokens'] is not None and result.get('total_s', 0) > 0 else None)
    return scrub(result, key)
