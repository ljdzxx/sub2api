"""Probe an EasyPay mapi.php from one host. Python 3.9+ and curl 7.70+.

python tools/easypay_probe.py --endpoint https://HOST/PATH/mapi.php --source server \
    --mode create --out server.jsonl [--start-at 2026-11-19T03:00:00+08:00]

create uses EPAY_PID, EPAY_KEY, EPAY_NOTIFY_URL, EPAY_RETURN_URL, EPAY_CLIENT_IP
and optional EPAY_CID. Use the same merchant, amount, CID and client IP on both
hosts. Each create request is a unique, unpaid upstream order. baseline sends
an empty form with no merchant details. Both modes bypass environment proxies
by default. Each sample uses a fresh connection, without redirects or retries.
"""
import argparse
import concurrent.futures
from contextlib import nullcontext
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import time
from urllib.parse import urlencode, urlsplit


LIMIT = 1024 * 1024
STATS_MARK = b'\n__CURL_PROBE_STATS__'
TIMINGS = ('namelookup', 'connect', 'appconnect', 'starttransfer', 'total')


def order_form(settings, amount, order_id):
    fields = {**settings, 'type': 'alipay', 'money': amount, 'name': 'payment-probe',
              'out_trade_no': order_id}
    key = fields.pop('key')
    text = '&'.join(f'{k}={v}' for k, v in sorted(fields.items()) if v)
    fields['sign'] = hashlib.md5((text + key).encode()).hexdigest()
    fields['sign_type'] = 'MD5'
    return urlencode(fields).encode()


def classify(body, stats, exit_code, mode):
    if exit_code:
        return {6: 'dns', 7: 'connect', 18: 'partial_transfer', 28: 'timeout',
                35: 'tls', 60: 'certificate', 63: 'size_limit'}.get(exit_code, 'transport_error')
    if not body.strip():
        return 'empty_body'
    try:
        data = json.loads(body)
    except (ValueError, UnicodeDecodeError):
        return 'invalid_json'
    if not 200 <= stats['http_code'] < 300:
        return 'http_error'
    if mode == 'baseline':
        return 'baseline_json'
    if not isinstance(data, dict) or data.get('code') != 1:
        return 'gateway_rejected'
    return 'created' if data.get('payurl') or data.get('qrcode') else 'missing_link'


def sample(args, number, settings, run, forms=None):
    order_id = f'EP{run}{number:03}'
    form = b''
    if forms is not None:
        form = forms[number - 1].encode('ascii')
    elif args.mode == 'create':
        form = order_form(settings, args.amount, order_id)
    cmd = [args.curl, '-q', '--silent', '--max-time', '10', '--max-filesize', str(LIMIT),
           '--data-binary', '@-', '--write-out', STATS_MARK.decode() + '%{json}', args.endpoint]
    if not args.env_proxy:
        cmd[2:2] = ['--noproxy', '*']
    started = dt.datetime.now(dt.timezone.utc)
    child_env = {k: v for k, v in os.environ.items() if not k.startswith('EPAY_')}
    result = subprocess.run(cmd, input=form, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                            env=child_env)
    body, marker, raw_stats = result.stdout.rpartition(STATS_MARK)
    if not marker:
        raise RuntimeError('curl did not return measurement data')
    stats = json.loads(raw_stats)
    row = dict(source=args.source, mode=args.mode, run=run, sample=number,
               started_utc=started.isoformat(timespec='milliseconds'),
               proxy_policy='environment' if args.env_proxy else 'direct',
               peer=stats.get('remote_ip'), http=stats['http_code'], curl_exit=result.returncode,
               content_type=(stats.get('content_type') or '').split(';')[0], bytes=len(body),
               result=classify(body, stats, result.returncode, args.mode))
    if args.mode == 'create':
        row.update(order_id=order_id, amount=args.amount)
    for key in TIMINGS:
        row[key + '_ms'] = round(1000 * stats['time_' + key], 2)
    return row


def main(argv=None, *, forms=None, run=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('--endpoint', required=True)
    p.add_argument('--source', required=True, choices=('server', 'local'))
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--mode', choices=('baseline', 'create'), required=True)
    p.add_argument('--count', type=int, default=10)
    p.add_argument('--amount', default='1.00')
    p.add_argument('--start-at', help='ISO 8601 timestamp with timezone, shared by both hosts')
    p.add_argument('--env-proxy', action='store_true')
    args = p.parse_args(argv)
    url = urlsplit(args.endpoint)
    if (url.scheme != 'https' or not url.hostname or url.username is not None or url.query or
            url.fragment or not url.path.endswith('/mapi.php')):
        p.error('expected an HTTPS mapi.php endpoint without credentials or query')
    if not 1 <= args.count <= 30 or not re.fullmatch(r'(?:[1-9]\d*|0)\.\d\d', args.amount) or args.amount == '0.00':
        p.error('expected 1..30 samples and a positive two-decimal amount')
    args.curl = shutil.which('curl.exe' if os.name == 'nt' else 'curl')
    if not args.curl:
        p.error('curl is required')
    settings = {}
    if args.mode == 'create' and forms is None:
        for field in ('pid', 'key', 'notify_url', 'return_url', 'clientip'):
            env = 'EPAY_' + ('CLIENT_IP' if field == 'clientip' else field.upper())
            if not os.environ.get(env):
                p.error('missing ' + env)
            settings[field] = os.environ[env]
        if os.environ.get('EPAY_CID'):
            settings['cid'] = os.environ['EPAY_CID']
    delay = 0
    if args.start_at:
        start = dt.datetime.fromisoformat(args.start_at)
        if start.utcoffset() is None:
            p.error('start-at requires timezone')
        delay = start.timestamp() - time.time()
        if delay < 0:
            p.error('start-at is in the past')
    run = run or secrets.token_hex(8)
    first = time.monotonic() + delay
    sink = nullcontext(sys.stdout) if str(args.out) == '-' else args.out.open('x', encoding='utf-8')
    with sink as output, concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        pending = set()
        for number in range(1, args.count + 1):
            deadline = first + 3 * (number - 1)
            while pending and time.monotonic() < deadline:
                done, pending = concurrent.futures.wait(pending, timeout=max(0, deadline-time.monotonic()),
                                                       return_when=concurrent.futures.FIRST_COMPLETED)
                for job in done:
                    print(json.dumps(job.result()), file=output, flush=True)
            time.sleep(max(0, deadline-time.monotonic()))
            pending.add(pool.submit(sample, args, number, settings, run, forms))
        for job in concurrent.futures.as_completed(pending):
            print(json.dumps(job.result()), file=output, flush=True)


if __name__ == '__main__':
    main()
