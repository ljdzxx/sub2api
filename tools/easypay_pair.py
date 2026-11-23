"""Run synchronized Windows/Linux EasyPay probes over an ordinary SSH login."""
import argparse
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import datetime as dt
import getpass
import ipaddress
import json
from pathlib import Path
import re
import secrets
import shutil
import statistics
import subprocess
import sys

import easypay_probe as probe

ROOT = Path(__file__).resolve().parents[1]
ENDPOINT = 'https://pay.88yyw.cn/xpay/epay/mapi.php'
REMOTE_MAIN = '''
if not shutil.which('curl'):
    sys.exit('服务器缺少 curl')
print('EPAY_READY', flush=True)
job = json.loads(sys.stdin.buffer.readline())
main(job['argv'], forms=job['forms'], run=job['run'])
'''


def ask(label, default=''):
    while True:
        value = input(label + (f' [{default}]' if default else '') + ' > ').strip() or default
        if value:
            return value


def make_job(source, settings, args, start, out):
    run = secrets.token_hex(8)
    argv = ['--endpoint', ENDPOINT, '--source', source, '--mode', 'create', '--out', str(out),
            '--count', str(args.count), '--amount', args.amount, '--start-at', start]
    forms = [probe.order_form(settings, args.amount, f'EP{run}{n:03}').decode('ascii')
             for n in range(1, args.count + 1)]
    return dict(argv=argv, run=run, forms=forms)


def collect(stream, dest):
    with dest.open('x', encoding='utf8') as output:
        for line in stream:
            row = json.loads(line)
            print(json.dumps(row), file=output, flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ssh', required=True, help='SSH login user@host or config alias')
    parser.add_argument('--identity', type=Path)
    parser.add_argument('--port', type=int, default=22)
    parser.add_argument('--count', type=int, default=10)
    parser.add_argument('--amount', default='1.00')
    args = parser.parse_args()
    if not 1 <= args.count <= 30 or not re.fullmatch(r'(?:[1-9]\d*|0)\.\d{2}', args.amount) or args.amount == '0.00':
        parser.error('count must be 1..30 and amount positive with two decimals')
    if not shutil.which('curl') or not shutil.which('ssh'):
        parser.error('本地需要 curl 和 ssh')
    ssh = ['ssh', '-T', '-o', 'ConnectTimeout=15', '-p', str(args.port)]
    if args.identity:
        ssh.extend(['-i', str(args.identity.expanduser()), '-o', 'IdentitiesOnly=yes'])
    ssh.extend(['--', args.ssh, "python3 -u -c 'import sys; exec(sys.stdin.buffer.readline())'"])
    dest = ROOT / '.codex/production-cache-audit' / ('epay-pair-' + dt.datetime.now().strftime('%Y%m%d-%H%M%S') + '-' + secrets.token_hex(2))
    print(f'正在连接 {args.ssh} ...', flush=True)
    proc = subprocess.Popen(ssh, stdin=subprocess.PIPE, stdout=subprocess.PIPE)
    try:
        source = Path(probe.__file__).read_text(encoding='utf8').split('if __name__ ==', 1)[0] + REMOTE_MAIN
        proc.stdin.write(('exec(' + ascii(source) + ')\n').encode('ascii'))
        proc.stdin.flush()
        if proc.stdout.readline().strip() != b'EPAY_READY':
            raise SystemExit('SSH 账号需能运行 python3 和 curl，请检查上面的 SSH 输出。')
        settings = {'pid': ask('实例 #2 商户 PID'), 'key': getpass.getpass('商户密钥 pkey（隐藏输入）> ').strip()}
        if not settings['key']:
            parser.error('商户密钥不能为空')
        settings.update(cid=input('支付宝通道 CID（实例为空则直接回车）> ').strip(),
                        notify_url=ask('异步通知 URL', 'https://sub2.jucodex.com/api/v1/payment/webhook/easypay'),
                        return_url=ask('同步返回 URL', 'https://sub2.jucodex.com/payment/result'),
                        clientip=str(ipaddress.ip_address(ask('填写你当前用户端的公网 IP，两端共用'))))
        dest.mkdir(parents=True)
        start = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=6)).isoformat()
        local = make_job('local', settings, args, start, dest / 'local.jsonl')
        remote = make_job('server', settings, args, start, '-')
        proc.stdin.write(json.dumps(remote).encode('ascii') + b'\n')
        proc.stdin.close()
        print(f'两端各 {args.count} 笔，每 3 秒启动一笔。结果目录 {dest}', flush=True)
        with ThreadPoolExecutor(max_workers=1) as workers:
            reader = workers.submit(collect, proc.stdout, dest / 'server.jsonl')
            try:
                probe.main(**local)
            finally:
                if sys.exc_info()[0] is not None:
                    proc.kill()
            reader.result()
        if proc.wait() != 0:
            raise SystemExit('服务器测试执行失败，已完成的样本保存在结果目录。')
    finally:
        if proc.poll() is None:
            proc.kill()
        proc.wait()
    for name in ('local', 'server'):
        rows = [json.loads(line) for line in (dest / f'{name}.jsonl').read_text().splitlines()]
        if len(rows) != args.count:
            raise SystemExit(f'{name} 只收到 {len(rows)}/{args.count} 条记录')
        print(f'{name}: {dict(Counter(r["result"] for r in rows))}, '
              f'总耗时中位数 {statistics.median(r["total_ms"] for r in rows):.0f} ms')


if __name__ == '__main__':
    main()
