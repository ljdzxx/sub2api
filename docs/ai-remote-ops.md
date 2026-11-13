# AI 远程运维手册

本手册记录 2026-11-13 已验证的生产连接方式、只读诊断权限和部署位置。供后续 AI 在收到“去服务器查一下”时直接开始工作。示例账号 `58` 来自本次故障，后续按任务替换。

这是现有环境的操作记录。容器状态、镜像、域名路由和业务策略会变化，每次排查须重新读取；不要将历史快照当作实时事实。

## 1. 快速开始：在本地 Windows PowerShell 执行

私钥保存在本地 Windows 工作站，SSH 客户端从这里发起连接。服务器上的 `root@Server` 终端没有这把私钥；不要把下面的 PowerShell 命令粘贴到 Linux 终端。

```powershell
Set-Location C:\vs_project\sub2api
$KeyPath = Join-Path $HOME '.ssh/ray_readonly_key'
if (-not (Test-Path -LiteralPath $KeyPath)) { throw "找不到本地私钥：$KeyPath" }
$Remote = 'ray@167.160.90.42'
$SshArgs = @(
  '-T', '-i', $KeyPath,
  '-o', 'BatchMode=yes',
  '-o', 'IdentitiesOnly=yes',
  '-o', 'ConnectTimeout=15',
  '-o', 'StrictHostKeyChecking=yes'
)
ssh @SshArgs $Remote 'diag status'
ssh @SshArgs $Remote 'diag host'
ssh @SshArgs $Remote 'diag inspect slave'
ssh @SshArgs $Remote 'diag inspect master'
```

后面的 PowerShell 示例复用 `$KeyPath`、`$Remote`、`$SshArgs`。每个失败的调用都应检查退出码和错误原因，不要将失败后的空输出当作“没有数据”。

| 项目 | 已验证值 |
| --- | --- |
| SSH 地址 | `167.160.90.42:22` |
| 远程诊断用户 | `ray` |
| 本地私钥 | `C:\Users\ray\.ssh\ray_readonly_key` |
| ED25519 主机指纹 | `SHA256:tJpVEhVWr19F/HteNsW6S1HQ5+MnfZqdA6tHoup2Ip8` |
| 本地仓库 | `C:\vs_project\sub2api` |
| 生产仓库 | `/opt/sub2api` |
| 生产部署目录 | `/opt/sub2api/deploy-ray` |
| 远程用户主目录 | `/home/ray` |

当前工作站已保存主机公钥。新工作站首次连接应先通过可信渠道核对指纹；指纹变化时先确认服务器是否更换，不能自动删除 known_hosts 记录或关闭校验。

## 2. 权限边界与诊断命令

`ray` 使用密钥和 SSH 强制命令入口，不是任意交互式 shell。已禁止端口转发和 TTY；没有通用 root 或 Docker 管理权限。普通排查不需要再次申请数据库密码，也不需要安装新服务。

| 远程命令 | 用途与限制 |
| --- | --- |
| `diag status` | 查看 master、slave、infra 三个 Compose 项目的容器状态 |
| `diag host` | 查看服务器时间、uptime 和进程列表；进程列表使用 `comm`，避免完整参数暴露秘密 |
| `diag inspect ROLE` | 查看容器状态、时间、镜像 ID、镜像创建时间和标签，不输出完整环境变量 |
| `diag top ROLE` | 查看指定容器的进程 |
| `diag logs ROLE HOURS` | 查看最近指定小时的日志，小时数 1–72，默认 24；最多末尾 10,000 行 |
| `diag sql` | 从标准输入读取 SQL，以只读角色连接业务数据库 |
| `diag redis DB COMMAND ARGS` | 用独立只读 ACL 用户执行允许的 Redis 命令 |

`ROLE` 为 `master`、`slave`、`postgres`、`redis`。例如：

```powershell
ssh @SshArgs $Remote 'diag top slave'
ssh @SshArgs $Remote 'diag logs slave 1'
```

高流量时，10,000 行可能只覆盖几分钟。没有搜到某个请求，不代表它不在更早的日志里。不要把 `HOURS=24` 理解为完整导出了 24 小时日志。日志本身可能包含请求内容或敏感字段，只取必要窗口，输出和落盘前脱敏。

“查一下”按只读范围执行。生产写操作、ACL 修改、清缓存、启停或部署服务需要用户明确授权；已有授权不重复确认。不要因为 master 在历史记录中被停止就自动启动它。

## 3. 读取服务器文件：SFTP

安装诊断入口后，默认 SFTP 调用曾直接断开；已验证可用的形式是明确指定 `/usr/lib/openssh/sftp-server`。`-s internal-sftp` 不能替代这里的可执行路径。服务器以 `-R` 只读模式运行，仍受文件权限限制。

在本地 PowerShell 执行：

```powershell
New-Item -ItemType Directory -Force .codex/production-cache-audit | Out-Null
$batch = @'
ls -la /opt/sub2api
ls -la /etc/nginx/conf.d
get /etc/nginx/conf.d/sub2.jucodex.com.conf C:/vs_project/sub2api/.codex/production-cache-audit/sub2.jucodex.com.conf
bye
'@
$batch | sftp -s /usr/lib/openssh/sftp-server -b - -i $KeyPath `
  -o BatchMode=yes -o IdentitiesOnly=yes -o ConnectTimeout=15 `
  -o StrictHostKeyChecking=yes $Remote
```

部署 `.env` 的读取已被拒绝，这是预期限制；不要尝试绕过。此账号不能上传补丁或执行任意远程 `cat`、`docker exec`。读取项目代码和 Nginx 配置时使用上述 SFTP 入口。

## 4. PostgreSQL：整个业务数据库只读

| 项目 | 配置 |
| --- | --- |
| 数据库 | `sub2api` |
| 镜像 | `postgres:18-alpine` |
| 诊断角色 | `sub2api_diag_ro` |
| 读取权限 | 按用户要求授予 `pg_read_all_data`，可读取业务库全部有权限的数据，而非只开放几张表 |
| 角色限制 | `INHERIT`；无超级用户、建库、建角色、复制或绕过 RLS 权限 |
| 只读保护 | `default_transaction_read_only=on`；入口同时设置只读事务选项 |
| 执行限制 | statement timeout 15 秒；lock timeout 2 秒；SQL 输入上限 64 KiB |
| 密码来源 | root 保管的配置，由诊断包装器注入；本地无需知道密码 |

`pg_read_all_data` 是集群角色成员资格，诊断包装器固定连接 `sub2api`；RLS 仍然有效。“全库可读”不等于允许修改数据或无视行级安全策略。不要读取无关用户凭据。

在本地 PowerShell 执行：

```powershell
$sql = @'
SELECT now(), current_database(), current_user,
       current_setting('transaction_read_only'),
       current_setting('TimeZone');
SELECT id, name, openai_experimental_features_enabled,
       openai_normal_input_cache_rules
FROM accounts WHERE id = 58;
'@
$sql | ssh @SshArgs $Remote 'diag sql'
```

预期用户为 `sub2api_diag_ro`，`transaction_read_only` 为 `on`。数据库时区已验证为 `Asia/Shanghai`，容器时间戳通常为 UTC；对齐证据时写清具体日期和时区。

SQL 入口允许以 `SELECT`、`WITH`、`EXPLAIN`、`SHOW` 开头的查询，使用 `psql -X -c`，不是交互式 psql。不要使用 `\d`、`\copy`、`\!` 等元命令；表结构查询使用 `information_schema`：

```powershell
$sql = @'
SELECT table_schema, table_name
FROM information_schema.tables
WHERE table_schema NOT IN ('pg_catalog', 'information_schema')
ORDER BY table_schema, table_name;
SELECT column_name, data_type
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = 'accounts'
ORDER BY ordinal_position;
'@
$sql | ssh @SshArgs $Remote 'diag sql'
```

查询大表时先明确账号、时间范围、字段和行数，避免 `SELECT *` 和无边界扫描。不要使用会执行写入的函数或以 `EXPLAIN ANALYZE` 包装写操作。

如果用户正在服务器 root 终端验证入口，应使用 Linux 命令，不要在服务器寻找 Windows 私钥：

```bash
sudo -u ray sudo -n /usr/local/sbin/sub2api-diag sql <<'SQL'
SELECT current_user, current_setting('transaction_read_only');
SQL
```

## 5. Redis：独立只读 ACL 用户

| 项目 | 配置 |
| --- | --- |
| 镜像 | `redis:8-alpine` |
| 诊断 ACL 用户 | `sub2api_ray_ro` |
| 本次业务缓存 DB | `0` |
| 键读取范围 | `%R~*`：所有键的读取权限；根据命令选择 DB |
| 命令范围 | `+@read -@dangerous -@scripting +ping +select +info +dbsize +time` |
| 执行限制 | 包装器超时 20 秒；禁写、删键、管理和脚本命令 |
| 密码来源 | root 保管并由包装器注入，禁止读取或输出配置中的密码 |

基本检查，在本地 PowerShell 执行：

```powershell
ssh @SshArgs $Remote 'diag redis 0 PING'
ssh @SshArgs $Remote 'diag redis 0 INFO keyspace'
ssh @SshArgs $Remote 'diag redis 0 DBSIZE'
ssh @SshArgs $Remote 'diag redis 0 TTL sched:meta:58'
```

按需查看键空间时用有边界的 `SCAN`，例如 `diag redis 0 SCAN 0 MATCH sched:meta:* COUNT 20`；游标返回为 0 才代表本次遍历结束，`COUNT` 不是严格的条数上限。不要使用 `KEYS *` 或清缓存来代替定位问题。诊断入口按空白拆分参数，不支持带空格的键/参数，也不接受额外 redis-cli 客户端选项。

### 安全检查账号策略字段

`sched:acc:58` 的完整 JSON 含账号凭据。不要直接把完整 `GET` 结果输出到工具日志、对话或文件。先在本地内存中解析，只输出需要的字段；同时检查字段是否缺失，不能把缺失和显式关闭当成同一情况。

```powershell
$AccountId = 58
try {
  foreach ($CacheKey in @("sched:acc:$AccountId", "sched:meta:$AccountId")) {
    $raw = ssh @SshArgs $Remote "diag redis 0 GET $CacheKey"
    if ($LASTEXITCODE -ne 0) { throw "Redis read failed: $CacheKey" }
    $rawText = $raw -join [Environment]::NewLine
    if ([string]::IsNullOrWhiteSpace($rawText)) {
      [pscustomobject]@{ Key = $CacheKey; Present = $false } | ConvertTo-Json
      continue
    }
    try { $item = $rawText | ConvertFrom-Json -ErrorAction Stop }
    catch { throw "Redis 返回值无法解析为 JSON：$CacheKey（不输出原文）" }
    [pscustomobject]@{
      Key = $CacheKey
      Present = $true
      ID = $item.ID
      FlagPresent = $item.PSObject.Properties.Name -contains 'OpenAIExperimentalFeaturesEnabled'
      RulesPresent = $item.PSObject.Properties.Name -contains 'OpenAINormalInputCacheRules'
      Enabled = $item.OpenAIExperimentalFeaturesEnabled
      Rules = $item.OpenAINormalInputCacheRules
    } | ConvertTo-Json -Depth 6
  }
}
finally {
  Remove-Variable raw,rawText,item -ErrorAction SilentlyContinue
}
```

### Redis 重启后的权限恢复

运行时 ACL 可能在 Redis 重启后丢失。出现 ACL 用户不存在、认证失败等情况，先核对错误和重启时间。以下恢复命令只供用户在服务器 root 终端执行，不属于 AI 的默认只读操作：

```bash
/usr/local/sbin/sub2api-enable-redis-readonly
sudo -u ray sudo -n /usr/local/sbin/sub2api-diag redis 0 PING
```

`ray` 不应自行修改 ACL。不要每次连接都重装权限。

## 6. 生产部署位置与访问路径

### 项目、容器和端口

生产代码在 `/opt/sub2api`，部署目录为 `/opt/sub2api/deploy-ray`。以下名称是本次确认的实例，后续先用 `diag status` 复核：

| Compose 项目 | 服务 | 容器 | 应用端口 |
| --- | --- | --- | --- |
| `sub2api-ray-master` | `master` | `sub2api-ray-master-master-1` | 宿主机 8080 → 容器 8080 |
| `sub2api-ray-slave` | `slave` | `sub2api-ray-slave-slave-1` | 宿主机 8081 → 容器 8080 |
| `sub2api-ray-infra` | `ray-postgres` | `sub2api-ray-infra-ray-postgres-1` | 共享数据库 |
| `sub2api-ray-infra` | `ray-redis` | `sub2api-ray-infra-ray-redis-1` | 共享缓存 |

两个应用共享 PostgreSQL 和 Redis，使用 `sub2api-ray-shared` 网络。`master/slave` 是应用部署名称，不代表数据库复制主从；两个完整应用都会运行自己的后台逻辑。某实例没有接收域名流量，并不代表它不会影响共享缓存。

部署目录中的主要入口：

| 文件/目录 | 用途 |
| --- | --- |
| `/opt/sub2api/deploy-ray/deploy.sh` | 双实例部署和生命周期管理 |
| `/opt/sub2api/deploy-ray/ops.sh` | 接续现有生产数据库的运维入口，使用前阅读对应 OPS 文档 |
| `/opt/sub2api/deploy-ray/compose.app.yml` | 应用 Compose 配置 |
| `/opt/sub2api/deploy-ray/compose.infra.yml` | PostgreSQL、Redis、网络和卷配置 |
| `/opt/sub2api/deploy-ray/.env` | 敏感部署配置，ray 无读取权限 |
| `/opt/sub2api/deploy-ray/compose.existing.local.json` | 接续现有库时生成的本机配置，如已生成；含生产秘密，不可展示或提交 |

详细部署流程见 [部署说明](../deploy-ray/README.md) 和 [现有生产环境运维说明](../deploy-ray/OPS.md)。现有生产环境不要为了消除报错执行 `init`、`infra-up` 或 `up all`；先确定使用的是哪个入口及当前部署方式。

### Nginx 实际路由

| 路径 | 本次检查结果 |
| --- | --- |
| `/etc/nginx/nginx.conf` | Nginx 主配置 |
| `/etc/nginx/conf.d/sub2.jucodex.com.conf` | `sub2_backend` 仅指向 `127.0.0.1:8081`，即 slave |
| `/etc/nginx/conf.d/master.jucodex.com.conf` | 指向 `127.0.0.1:8080`，即 master |
| `/var/log/nginx/sub2.jucodex.com/access.log` | 站点访问日志；能否读取取决于文件权限 |
| `/var/log/nginx/sub2.jucodex.com/error.log` | 站点错误日志；能否读取取决于文件权限 |

本次 `sub2.jucodex.com` 并未在 8080/8081 两个应用之间负载均衡。下次调查仍应通过 SFTP 重新读取配置，从域名 → upstream → 宿主机端口 → 容器建立对应关系，而不是根据 master/slave 名称猜测流量归属。

### 历史状态与 deploy.sh 报错

截至 2026-11-13 约 15:55（Asia/Shanghai），用户已停止旧 master，退出码为 0；slave、PostgreSQL、Redis 健康。该状态只是本次收尾快照，下次须重新确认。

服务器旧版 `bash deploy.sh stop master` 会在 Compose 展开配置时要求 `ADMIN_PASSWORD`，导致停止命令失败。它不是 SSH 密码，也不说明现有应用缺少管理员密码。

本地已经修改 `stop/restart/logs/status`：按 Compose 项目和服务标签定位现有容器，直接使用 Docker，不读取 `.env`；部署类命令保留配置校验。该修复的 38 项部署测试已通过，但本次并未将本地补丁部署到生产。不得假定服务器已有此修复。

如用户明确要求停止旧 master，并确认上述容器身份，管理员可在服务器上使用不展开 Compose 配置的命令：

```bash
docker stop --timeout 60 sub2api-ray-master-master-1
```

这属于生产变更，ray 的诊断入口不能执行。不要自动重启旧 master；重新上线前应确认它使用兼容版本，避免再次写入缺失字段的共享缓存。

## 7. 服务器权限入口与本地安装脚本

以下文件供定位权限问题使用，含秘密的配置不要尝试读取、下载或展示：

| 服务器路径 | 用途 |
| --- | --- |
| `/etc/ssh/sshd_config` 的 `Match User ray` 段 | 密钥认证、强制诊断入口、禁用转发与 TTY |
| `/usr/local/bin/sub2api-diag-entry` | 分派 SSH 原始命令和只读 SFTP |
| `/usr/local/sbin/sub2api-diag` | 容器、进程、日志、SQL 和 Redis 诊断入口 |
| `/etc/sudoers.d/sub2api-diag` | 允许 ray 调用特定诊断包装器 |
| `/etc/sub2api-diag.conf` | root 保管的数据库连接配置，含密码 |
| `/usr/local/sbin/sub2api-redis-ro` | Redis 只读查询包装器 |
| `/etc/sub2api-redis-ro.conf` | root 保管的 Redis 诊断密码 |
| `/usr/local/sbin/sub2api-enable-redis-readonly` | 管理员恢复 Redis 只读 ACL 的入口 |

本地初始化脚本保留在 `.codex/sub2api-readonly-setup.sh` 和 `.codex/sub2api-redis-readonly-setup.sh`。它们位于忽略目录，不保证在新克隆中存在，也不应每次排查都重跑。用户曾另存 Redis 脚本到 `C:\Users\ray\Downloads\redis-readonly.sh`。

## 8. 已遇到的问题与正确处理

| 现象 | 原因与处理 |
| --- | --- |
| `root@167.160.90.42's password:` | 使用了 root 账号，且原 scp 命令没有指定密钥。ray 的密钥仅证明 ray 的登录权限，不能假设可用于 root；常规排查使用本文 ray 入口 |
| `Identity file /root/.ssh/ray_readonly_key not accessible` | 在服务器 root shell 执行了本地连接命令。回到 Windows PowerShell 使用本地私钥；不要复制私钥到服务器 |
| `Permission denied (publickey)` | 检查客户端用户名、密钥路径、已安装公钥和 SSH 策略；不要改用猜测密码或修改服务器认证方式 |
| `Host key verification failed` | 检查是否首次连接或主机公钥发生变化，先核验指纹，不自动关闭校验 |
| SFTP 立即关闭连接 | 使用本文显式 `-s /usr/lib/openssh/sftp-server` 形式 |
| `.env`、凭据文件读取被拒绝 | 属于预期权限边界；使用 diag 包装器，不绕过 |
| Bash 提示 `SELECT ...: command not found` | 将 PowerShell 的字符串管道复制到了 Bash；使用第 4 节 Linux heredoc 示例 |
| `: invalid option namepefail` | Windows CRLF 与 Linux Bash 脚本不兼容，且不能用 `sh` 执行要求 Bash 的脚本 |
| Redis 认证/ACL 错误 | 核对 Redis 是否重启，必要时由管理员执行第 5 节恢复入口 |
| `ADMIN_PASSWORD is missing a value` | 旧版部署脚本在停止等操作时也展开 Compose；见第 6 节，不要重新初始化生产 |

用户确实要安装/恢复 Redis 权限且脚本在 `/tmp/redis-readonly.sh` 时，可在 Linux root 终端修正换行并执行：

```bash
sed -i 's/\r$//' /tmp/redis-readonly.sh
bash -n /tmp/redis-readonly.sh && bash /tmp/redis-readonly.sh
```

这会执行权限安装，不属于日常只读排查。

## 9. 本次账号 #58 故障：后续排查背景

以下记录来自 2026-11-13 的现场证据，时间均为 Asia/Shanghai。它们用于解释本次修复和检查思路，不用于覆盖以后用户的真实配置。

- 当时数据库启用 OpenAI 实验性功能，规则为 99% 概率转移 5%、1% 概率转移 8%；问题也曾在设为 100% 时出现。
- Redis `sched:acc:58`、`sched:meta:58` 在约 15:34、15:37 缺少 `OpenAIExperimentalFeaturesEnabled` 和 `OpenAINormalInputCacheRules`，约 15:39 又出现这两个字段。
- master 镜像构建于 2026-11-09；slave 镜像构建于 2026-11-12。策略功能于 2026-11-12 的提交 `21f5dc935` 引入。旧应用与新应用共享相同缓存键。
- 旧格式 JSON 缺字段时，原解码逻辑得到 `false/nil` 并视为缓存命中，策略在进入随机选择之前就被跳过。因此即使概率为 100%，也会表现为未转写。
- Nginx 和受影响请求日志指向 slave；无前台流量的旧 master 仍可能通过后台任务写共享缓存。现场字段反复消失/恢复、版本差异和序列化代码共同支持此机制，但没有逐次记录 Redis 写入者身份。
- 用户停止旧 master 后，15:50:00–15:52:59 的 262 条账号 #58 记录全部存在策略快照，计算出的账号阶段转写增量全部一致。原规则样本中的 13 条零增量都是按规则计算后取整为零，不能据此判定策略未执行。
- 用户随后将规则改为 70% 转移 5%、20% 转移 10%、10% 转移 0%。15:53 的缓存与数据库一致。下次必须读取当时规则及审计快照，不能继续假定是 99%。

本地兼容修复：对于 OpenAI API Key 账号，缓存缺少上述必要字段时返回缓存未命中，使用现有受限数据库回退；显式关闭和其他账号类型仍是有效缓存。本地回归测试已复现原问题并验证修复。生产应用补丁尚未由本次 AI 部署；历史用量数据未修改。

相关代码与说明：

- [缓存读取实现](../backend/internal/repository/scheduler_cache.go)
- [缓存策略回归测试](../backend/internal/repository/scheduler_cache_openai_policy_unit_test.go)
- [策略行为与计费说明](openai-cache-read-deduction.md)
- 本机证据：`.codex/production-cache-audit/findings-2026-11-13.md`、`cache-policy-samples.jsonl`。这些文件不随 Git 克隆分发。

## 10. 下次 AI 的执行顺序

1. 阅读本手册，检查本地私钥是否存在，用 `diag status`、`diag host` 确认连接、时间与容器状态。已有连接方式无需再次向用户索取凭据。
2. 用 `diag inspect` 确认正在运行的镜像与创建时间；如涉及域名流量，通过 SFTP 读取实际 Nginx upstream，定位对应应用。
3. 用 `diag sql` 确认数据库身份和只读状态，读取相关账号当前策略、必要的表结构及指定时间段的审计记录。
4. 用 `diag redis` 核对相关缓存、字段是否存在和 TTL；含账号凭据的 JSON 只输出筛选后的诊断字段。对间歇问题进行有限次数、带时间戳的采样。
5. 从相应容器获取限定窗口的日志，按请求 ID 对齐数据库与缓存证据，留意日志行数上限及 UTC/北京时间差异。
6. 只保存必要的脱敏证据到 `.codex/production-cache-audit/`，写清时间范围、实际配置、已证实事实与未证实推断。
7. 需要修复时先在本地完成可审阅的补丁与验证，明确哪些已部署、哪些仍是本地改动。只有在用户授权范围内执行生产变更。

本文不记录私钥内容、数据库/Redis 密码或账号令牌。新工作站应使用用户正式配置的密钥与服务器权限，不从文档推导或绕过授权。
