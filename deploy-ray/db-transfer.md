# 数据库迁移：旧 deploy Docker 实例迁移到新服务器

本文适用于：旧服务器上已有通过旧 deploy 目录和 Docker 运行的单实例，现在迁移到新服务器，并在新服务器使用 deploy-ray/deploy.sh 创建新的 PostgreSQL、Redis 和应用容器。

迁移使用 PostgreSQL 逻辑备份/恢复，不直接复制 PostgreSQL 数据目录。旧服务器在切换前保持原数据库不变，新服务器恢复并验证后再切换流量。

## 迁移顺序

~~~text
旧服务器停止旧应用
  -> pg_dump -Fc 导出旧 PostgreSQL
  -> 传输 dump 和必要密钥
  -> 新服务器 deploy.sh init/check/build/infra-up
  -> pg_restore 恢复到新 PostgreSQL
  -> deploy.sh up master，验证迁移
  -> deploy.sh up slave，完成双实例启动
~~~

ops.sh 用于同一台服务器上复用已有 PostgreSQL/Redis；本次迁移完成后新服务器拥有新的基础服务，因此使用 deploy.sh，不要用 ops.sh 接续旧容器。

## 一、旧服务器：停止应用并制作备份

### 1. 确认容器

以下名称按实际容器名替换：

~~~bash
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'

OLD_APP=sub2api
OLD_PG=sub2api-postgres
OLD_REDIS=sub2api-redis
docker inspect "$OLD_APP" "$OLD_PG" "$OLD_REDIS" >/dev/null
~~~

### 2. 保存旧配置和密钥

保存旧 deploy 目录的 .env，并记录：

- JWT_SECRET
- TOTP_ENCRYPTION_KEY
- REDIS_DB
- POSTGRES_DB、POSTGRES_USER

迁移已有数据库时，JWT_SECRET 和 TOTP_ENCRYPTION_KEY 必须带到新服务器；否则已有令牌、TOTP 或加密配置可能无法继续使用。

~~~bash
chmod 600 /旧部署目录/.env
~~~

### 3. 停止旧应用

只停止旧应用，不停止 PostgreSQL/Redis：

~~~bash
docker stop "$OLD_APP"
docker inspect --format '{{.State.Running}}' "$OLD_APP"
~~~

不要删除旧应用、PostgreSQL、Redis 容器或数据卷。

### 4. 导出 PostgreSQL

pg_dump -Fc 是可移植的自定义格式，适合恢复到新 PostgreSQL：

~~~bash
umask 077
mkdir -p /旧部署目录/backups
BACKUP=/旧部署目录/backups/sub2api-$(date -u +%Y%m%dT%H%M%SZ).dump
docker exec "$OLD_PG" sh -c 'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$BACKUP"
test -s "$BACKUP"
docker exec -i "$OLD_PG" pg_restore --list < "$BACKUP" >/dev/null
sha256sum "$BACKUP"
~~~

记录哈希，传输后再次计算并比对。该备份包含业务数据库的表结构和数据，不包含 Redis 数据、集群级角色、应用目录和运维密钥。

### 5. 传输备份和密钥

~~~bash
scp "$BACKUP" 新服务器:/srv/sub2api-migration/
scp /旧部署目录/.env 新服务器:/srv/sub2api-migration/old.env
~~~

新服务器上执行：

~~~bash
chmod 600 /srv/sub2api-migration/*
sha256sum /srv/sub2api-migration/sub2api-*.dump
~~~

## 二、新服务器：准备 deploy-ray

### 1. 获取源码并初始化配置

~~~bash
git clone --branch ray https://github.com/ljdzxx/sub2api.git /srv/sub2api
cd /srv/sub2api/deploy-ray

## 如果沿用旧的.env，本步可跳过 ##
bash deploy.sh init
nano .env
~~~

配置要求：

1. 把旧环境的 JWT_SECRET、TOTP_ENCRYPTION_KEY 填入新 .env。
2. POSTGRES_USER、POSTGRES_DB 使用旧业务库的值，默认是 sub2api。
3. PostgreSQL/Redis 可以使用 deploy.sh init 生成的新密码，也可以继续使用旧密码；关键是 .env 与新容器实际使用的密码必须一致。
4. REDIS_DB 按旧环境保持一致。
5. 应用连接池设置为：

~~~dotenv
POSTGRES_MAX_CONNECTIONS=200
DATABASE_MAX_OPEN_CONNS=50
DATABASE_MAX_IDLE_CONNS=10
~~~

ADMIN_EMAIL 和 ADMIN_PASSWORD 只对空数据库初始化有效；恢复已有数据库后不会重置现有管理员账号。

如果直接复制旧 .env，先确认变量与 deploy-ray/.env.example 兼容，并确认新 PostgreSQL/Redis 的密码不会与 .env 不一致。

### 2. 校验并构建

~~~bash
bash deploy.sh check
bash deploy.sh build all
~~~

此时不要执行 bash deploy.sh up all，因为新数据库尚未恢复。

## 三、新服务器：启动基础服务并恢复数据库

### 1. 只启动 PostgreSQL/Redis

~~~bash
bash deploy.sh infra-up
bash deploy.sh status
~~~

确认 ray-postgres 和 ray-redis 都是 healthy 后继续。

### 2. 恢复 dump

~~~bash
BACKUP=/srv/sub2api-migration/sub2api-xxxxxxxxTxxxxxxZ.dump
test -s "$BACKUP"
~~~

使用容器内的环境变量执行恢复，避免把 .env 当作 shell 脚本读取：

~~~bash
docker compose --project-name sub2api-ray-infra --env-file .env -f compose.infra.yml exec -T ray-postgres sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --no-owner --no-privileges --exit-on-error' < "$BACKUP"
~~~

恢复失败时停止处理，先确认目标数据库、用户、备份文件和 PostgreSQL 版本，不要在未确认恢复结果时启动应用。

### 3. 验证恢复结果

~~~bash
docker compose --project-name sub2api-ray-infra --env-file .env -f compose.infra.yml exec -T ray-postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atqc "SELECT current_database(), (SELECT count(*) FROM public.users), (SELECT count(*) FROM public.accounts), (SELECT count(*) FROM public.schema_migrations);"'
~~~

确认数据库名称正确，users、accounts、schema_migrations 存在且有数据，数量与旧服务器大致一致，并且 PostgreSQL/Redis 健康。

## 四、数据库恢复完成后执行 deploy.sh

推荐先启动 master，验证后再启动 slave：

~~~bash
cd /srv/sub2api/deploy-ray
bash deploy.sh up master
curl -fsS http://127.0.0.1:8080/health
bash deploy.sh logs master

bash deploy.sh up slave
curl -fsS http://127.0.0.1:8081/health
bash deploy.sh status
~~~

确认需要同时启动时，也可以在恢复完成后执行：

~~~bash
bash deploy.sh up all
~~~

up all 只负责启动基础服务和应用，不会从 dump 自动恢复数据库；所以必须在 pg_restore 成功之后执行。

应用首次连接恢复后的数据库时，可能执行项目已有 schema migration。迁移期间共享数据库锁，不要同时从多个终端启动两个全新实例。

启动后检查：

~~~bash
bash deploy.sh logs master
bash deploy.sh logs slave
curl -fsS http://新服务器IP:8080/health
curl -fsS http://新服务器IP:8081/health
~~~

## 五、切换流量和保留旧服务器

切换 DNS、反向代理或负载均衡前，确认：

- 两个健康检查均成功；
- 管理员可以登录，旧用户和 API Key 可见；
- 关键设置、配额和历史用量存在；
- 日志无数据库连接、迁移或 TOTP 解密错误；
- 新服务器可以再次备份：

~~~bash
bash deploy.sh backup
~~~

切换完成前不要在旧服务器重新启动应用，避免新旧实例同时写入两套数据库。旧 PostgreSQL、Redis、应用容器和原始 .env 至少保留到新服务器验证完成并完成一次新备份。

## 六、Redis 说明

pg_dump 不包含 Redis。Redis 中的会话、缓存、分布式锁和短期状态通常可以在新服务器上从空 Redis 开始，用户可能需要重新登录。

如果必须保留 Redis 数据，需在停旧应用后单独执行 Redis 官方导出/恢复流程，并在新 .env 中保持相同的 REDIS_DB。不要在未确认 Redis 版本和数据格式兼容前直接复制正在运行的 Redis 数据目录。

## 七、常见错误

### 恢复前执行 bash deploy.sh up all

正确顺序是：

~~~text
deploy.sh init -> check -> build all -> infra-up -> pg_restore -> up master -> up slave
~~~

### 只复制 PostgreSQL 数据目录

不要把旧 PostgreSQL 原始数据目录直接挂载到新容器。使用 pg_dump -Fc 和 pg_restore；物理迁移只适用于完全一致的 PostgreSQL 大版本、已停止的服务和经过验证的流程。

### 生成新的 TOTP/JWT 密钥

迁移已有业务库时，必须恢复旧的 TOTP_ENCRYPTION_KEY 和 JWT_SECRET，否则已有加密配置和令牌可能无法继续使用。

### 恢复到错误数据库

恢复前核对 POSTGRES_DB、POSTGRES_USER 和目标容器；恢复后检查 users、accounts、schema_migrations。不要用空数据库验证通过来代替业务数据核对。

### 执行 docker compose down -v

迁移期间不要执行 docker compose down -v，它会删除新环境的数据库/Redis 数据卷。
