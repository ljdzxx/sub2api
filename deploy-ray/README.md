# ray 分支：Linux 双实例 Docker Compose 部署

本目录从当前源码构建包含 `ray` 专有功能的镜像，部署两个可以独立启停、更新的应用实例，并共享一套 PostgreSQL 和 Redis。

## 部署结构

| Compose 项目 | 服务 | 宿主机端口 | 数据 |
| --- | --- | --- | --- |
| `sub2api-ray-master` | `master` | `8080` | 独立 `master_data` 卷 |
| `sub2api-ray-salve` | `salve` | `8081` | 独立 `salve_data` 卷 |
| `sub2api-ray-infra` | `ray-postgres`、`ray-redis` | 不向宿主机发布 | 共享数据库卷和 Redis 卷 |

服务名按需求使用 **`salve`**，脚本参数也使用这个拼写。两个应用容器内部都监听 8080，通过宿主机端口区分。

三个项目通过 `sub2api-ray-shared` 网络连接。应用项目引用外部网络，不拥有基础服务；单独 `up/stop/restart` 某个应用不会创建、停止或重建另一个应用或数据库/Redis。

`master/salve` 是部署名称，两者运行完整程序。它们不是数据库复制主从，不会因此自动分配程序内部后台任务，也没有自动流量切换。后台任务沿用项目已有的分布式锁/选主实现。

**独立的是进程和部署生命周期，业务数据并不隔离。** 用户、账号、API Key、设置、用量来自同一个数据库；Redis DB、会话和并发状态也共享。任一实例修改公共设置会影响另一实例，数据库/Redis 故障会影响两者。

## 环境要求

- Linux，Bash 4+、Git、OpenSSL。
- Docker Engine（启用 BuildKit）、Docker Buildx 及 Docker Compose 插件 **v2.20+**。
- 当前用户可以执行 `docker`；8080 和 8081 未被其他服务占用。
- 完整 `ray` 分支源码。首次构建包含前端和 Go 后端，建议准备至少 8 GB 可用内存及足够磁盘空间。

检查安装：

```bash
docker version
docker buildx version
docker compose version
openssl version
```

## 首次部署

如果 GitHub 上还没有 `ray` 分支，先在维护源码的机器执行 `git push -u origin ray`。部署脚本不会执行 Git 推送。

在 Linux 服务器执行：

```bash
git clone --branch ray https://github.com/ljdzxx/sub2api.git
cd sub2api/deploy-ray

# 生成一次公共配置和随机密码，不会覆盖已有 .env
bash deploy.sh init

# 至少修改 ADMIN_EMAIL；管理员初始密码在 ADMIN_PASSWORD 中
nano .env

bash deploy.sh check
bash deploy.sh build all
bash deploy.sh up all
bash deploy.sh status

curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8081/health
```

访问：

- master：`http://服务器IP:8080`
- salve：`http://服务器IP:8081`

两边使用同一个管理员账号。`ADMIN_EMAIL/ADMIN_PASSWORD` 用于空数据库初始化；修改环境变量不会重置已有管理员密码。

`up all` 先等待 PostgreSQL/Redis 健康，然后启动 master，待 master 初始化完成并健康后再启动 salve。首次空数据库安装不要从两个终端同时启动两个应用：数据库迁移有锁，但初始化管理员仍应按顺序完成。

也可以让 salve 首先启动，完全不启动 master：

```bash
bash deploy.sh build salve
bash deploy.sh infra-up
bash deploy.sh up salve
```

`init` 创建的 `.env` 权限为 600，包含数据库密码、Redis 密码、JWT 密钥、TOTP 密钥和管理员初始密码。两实例始终读取这一份文件；不要分别生成密钥。自定义密码含 `$` 时，在 `.env` 中用单引号包围该值。`.env` 不作为 Shell 脚本执行。

## 独立管理

```bash
# 只启动/重建所选应用，基础服务必须已健康
bash deploy.sh up master
bash deploy.sh up salve

# 停止一个不会停止另一个
bash deploy.sh stop master
bash deploy.sh stop salve

# 只重启已有容器，不应用新的镜像或 .env 配置
bash deploy.sh restart master
bash deploy.sh restart salve

# 跟踪日志，Ctrl+C 只退出查看
bash deploy.sh logs master
bash deploy.sh logs salve
bash deploy.sh logs infra

# 停止两个应用，保留数据库、Redis 和所有卷
bash deploy.sh stop all
```

改变某个应用镜像或应用环境变量后，使用 `up master` 或 `up salve` 应用配置。单实例命令只读取基础服务的健康状态，**不会自动调用 `infra-up`**。

启动等待默认 600 秒，首次迁移耗时较长时可以增加：

```bash
RAY_WAIT_TIMEOUT=1200 bash deploy.sh up master
```

## 更新其中一个应用

先备份共享数据库，再更新源码。新旧版本必须能同时使用同一份数据库结构。

```bash
cd /你的路径/sub2api/deploy-ray
bash deploy.sh backup

# 示例：保留默认 salve 镜像的旧版本标签
docker image tag sub2api-ray:salve sub2api-ray:salve-before-update

cd ..
git switch ray
git pull --ff-only origin ray
cd deploy-ray

bash deploy.sh build salve
bash deploy.sh up salve
curl -fsS http://127.0.0.1:8081/health

# 验证后再更新 master
bash deploy.sh build master
bash deploy.sh up master
```

`build salve` 只更新 `SALVE_IMAGE` 对应的本地镜像标签；不会重启任何容器。`build all` 构建一次，再将同一镜像标记为两个实例的镜像。若使用自定义版本标签，更新 `.env` 中对应的 `MASTER_IMAGE`/`SALVE_IMAGE`，并为旧镜像保留独立标签。

为保持镜像更新独立，请让 `MASTER_IMAGE` 和 `SALVE_IMAGE` 使用不同标签；不要把两者改成同一个标签。

**共享数据库的 schema 迁移会影响两个应用。** 不能将连着生产数据库的 salve 当作隔离的数据库升级测试环境。不兼容迁移应在维护窗口停止两个应用并按版本要求升级；只回退 Docker 镜像不会回退数据库结构。

持续维护本分支时，从源码构建并部署镜像；使用上游预构建镜像或程序内的上游更新功能，可能替换掉 `ray` 专有功能。

## 配置与持久化

- `.env.example` 列出可调整配置，实际部署使用 `.env`。`BIND_HOST=127.0.0.1` 可用于只允许本机反向代理访问。
- 数据库默认最大连接数 200，每个应用池默认最大 50；调整时按两个应用和后台任务的连接总和计算。
- 两边必须使用相同的 `JWT_SECRET`、`TOTP_ENCRYPTION_KEY`、数据库参数、Redis 密码及 `REDIS_DB`。
- PostgreSQL 显式设置 `PGDATA=/var/lib/postgresql/data`，避免 PostgreSQL 18 数据落入未挂载的位置；Redis 开启 AOF 持久化。
- `/app/data` 分别存放在两个应用自己的卷中，不共享配置文件写入。需要跨实例访问的上传文件/本地资源应另外配置共享对象存储或专用共享目录。
- 共享密钥、数据库/Redis 密码变更需要协调两实例和基础服务。不要仅修改 `.env` 就认为已有数据库用户密码也会自动修改。
- 默认固定项目名和网络名适用于同一 Docker 主机的一套双实例部署。迁移目录不改变卷名；不要在同一主机再用相同名称部署另一套环境。

备份命令：

```bash
bash deploy.sh backup
```

文件保存在 `deploy-ray/backups/`，采用 `pg_dump -Fc` 格式。备份只包含 PostgreSQL；另行保管 `.env`、需要保留的应用卷和 Redis 数据。需要恢复时先停止两个应用，再使用 `pg_restore` 对共享数据库操作，确认恢复完成后逐个启动应用。

脚本不提供删除数据卷的命令。需要停止基础服务时，先停两个应用，再显式执行：

```bash
bash deploy.sh stop all
docker compose --project-name sub2api-ray-infra --env-file .env -f compose.infra.yml stop

# 恢复
bash deploy.sh infra-up
bash deploy.sh up master
bash deploy.sh up salve
```

不要对共享 Redis 执行 `FLUSHALL`，也不要在需要保留数据时运行 `docker compose down -v`。

## 文件与验证

- `compose.infra.yml`：共享 PostgreSQL、Redis、网络和持久化卷。
- `compose.app.yml`：两个应用的公共配置、独立端口及独立数据卷。
- `deploy.sh`：初始化、构建、独立启停、日志、健康检查、备份。
- `tests/test_deploy.py`：使用 Docker 命令替身验证脚本不会误操作另一实例/基础服务，不创建真实容器。

```bash
bash -n deploy.sh
python3 -m unittest discover -s tests -v
bash deploy.sh check
```

`check` 使用真实 Compose 解析器校验配置，并且不输出展开后的密码。构建或启动失败时先查看对应实例日志；当前脚本不会自动删除卷或重置数据来恢复。
