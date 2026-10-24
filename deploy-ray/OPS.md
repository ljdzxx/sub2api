# 现有生产环境的一键运维：ops.sh

适用于已经通过旧部署运行以下容器的服务器：

- `sub2api`：旧应用，可先停止，但首次接续前不要删除容器。
- `sub2api-postgres`：原 PostgreSQL，保持运行。
- `sub2api-redis`：原 Redis，保持运行。

**只需要下载并执行一个 `ops.sh` 文件，不需要手工生成 Compose 配置、复制密码、切换源码目录。** 脚本包含完整运维逻辑，只依赖 Linux、Bash、Python 3、Git、Docker Engine、Buildx 和 Docker Compose v2.20+。

## 首次使用

先把本地 `ray` 分支推送到 GitHub，使服务器能够下载这版脚本和构建源码：

```bash
git push -u origin ray
```

然后在生产服务器执行：

```bash
# 只停旧应用；不要停止/删除 PostgreSQL、Redis
docker stop sub2api

# 固定放在这个位置，之后一直运行同一份入口
curl -fL https://raw.githubusercontent.com/ljdzxx/sub2api/ray/deploy-ray/ops.sh \
  -o /root/sub2api-ops.sh

bash /root/sub2api-ops.sh
```

菜单选择：

| 选项 | 操作 |
| --- | --- |
| 1 / 2 / 3 | 启动 master / salve / 两者 |
| 4 / 5 / 6 | 停止 master / salve / 两者 |
| 7 / 8 / 9 | 更新 master / salve / 两者 |
| 10 / 11 | 查看 master / salve 日志 |
| 12 | 查看旧应用、原基础服务及新实例状态 |
| 13 | 只读备份原数据库 |
| 14 | 首次接续或重新核验原库 |

第一次选启动即可自动完成接续、获取 `ray` 源码、构建镜像、只读备份和启动。master 使用 8080，salve 使用 8081；新容器名分别为 `sub2api-ray-master`、`sub2api-ray-salve`。旧 `sub2api` 不会被脚本删除或自动启动。

构建时可能下载 PostgreSQL 镜像，这是项目 Dockerfile 提取 `psql/pg_dump` 客户端的构建阶段，不会启动新的数据库容器。

## 后续更新只运行同一个文件

```bash
bash /root/sub2api-ops.sh
```

选择更新目标即可。也支持无菜单命令，便于定时维护或复制执行：

```bash
bash /root/sub2api-ops.sh start master
bash /root/sub2api-ops.sh start salve
bash /root/sub2api-ops.sh stop master
bash /root/sub2api-ops.sh stop salve
bash /root/sub2api-ops.sh update master
bash /root/sub2api-ops.sh update salve
bash /root/sub2api-ops.sh update all
bash /root/sub2api-ops.sh restart master
bash /root/sub2api-ops.sh status
bash /root/sub2api-ops.sh logs salve
```

`start` 使用实例已保存的镜像；首次缺少镜像时才自动拉源码构建。`update` 会快进更新受管 `ray` 源码、构建新镜像、备份原库，再只重建所选实例。`restart` 只重启已有实例，不拉取代码。

更新前的拉代码、构建或备份失败，都不会停止或替换正在运行的新版实例。备份文件生成后还会执行 `pg_restore --list` 检查格式；这是只读检查，不会恢复或修改数据库。新版本启动失败时保留日志和旧镜像信息，不自动回退数据库。

## 原数据库保护规则

1. **不提供 PostgreSQL/Redis 的 Compose 服务定义，不创建或重建它们，也不创建新的共享网络。** 应用只使用现有外部网络。
2. 读取原应用实际环境，复用数据库名、账号密码、Redis DB/密码和 JWT/TOTP 密钥。密码缺失或连接目标不能确认时退出，不生成替代连接配置。
3. 首次接续通过只读 SQL 检查 `users/accounts/api_keys/settings/schema_migrations` 已存在，且已有用户和迁移记录。目标不是原业务库时退出，不初始化空库。
4. 保存原库 OID 和 PostgreSQL/Redis 数据挂载标识。后续操作发现库标识或挂载变化时退出，不自动改接到其他数据目录。
5. 应用固定使用 **`SKIP_SETUP=true`、`AUTO_SETUP=false`**，不进入创建数据库或首次安装流程。
6. **每次容器启动都有独立检查脚本**，在 Go 程序启动之前只读核验原库，包括 Docker 自动重启的情况。空库、错误库或关键表缺失都不会继续启动。
7. 没有 `down -v`、清库、删除卷、重置密码或重新初始化数据库的菜单和命令。

这里阻止的是错误建库、空库初始化和误接数据目录。**正常版本升级仍会在原库执行已有迁移，业务运行也会正常写入数据。** 自动备份用于留存升级前恢复点，不能把脚本检查视为任何应用版本都不会出现数据问题的保证。

## 文件位置和数据目录

默认在脚本所在目录创建 `.ray-ops/`，例如 `/root/.ray-ops/`：

- `connection.json`：原连接信息、库标识、数据挂载和实例镜像信息，权限 600。
- `original-config.yaml`：旧配置副本，权限 600。
- `source/`：自动维护的 `ray` 源码，**只是源码，不是新数据库目录**。
- `master.compose.json` / `salve.compose.json`：独立项目配置，包含密钥，权限 600。
- `salve-data/`：salve 的独立应用数据目录；master 直接沿用原 `/app/data` 挂载。
- `backups/`：原数据库只读导出，按时间保留；脚本不会自动删除历史备份。
- `existing-db-guard.sh`：容器启动前检查，只读挂载到两个应用。

整个状态目录权限为 700。不要上传该目录、移动运维入口后改用另一个状态目录，或手工删除连接记录来绕过检查。按需清理已确认不再需要的旧备份和镜像，避免磁盘耗尽。

master/salve 启停独立，但同用原数据库和 Redis：配置、账号、配额和正常数据库迁移会作用于共享业务状态。salve 不会自动复制旧应用目录里的所有附件/自定义文件，仅继承原配置；如原配置引用额外本地文件，应在使用 salve 前将这些文件放入其应用数据目录，或使用共享对象存储。

## 少数需要显式指定的情况

如果容器名不同，第一次运行时设置：

```bash
RAY_OLD_APP=旧应用名 RAY_POSTGRES=原Postgres容器名 RAY_REDIS=原Redis容器名 \
  bash /root/sub2api-ops.sh
```

多个共同网络时用 `RAY_EXISTING_NETWORK=实际网络名` 指定；首次建立记录后会固定使用保存的网络。不要切换成新网络。

`RAY_OPS_STATE_DIR` 可指定固定状态目录；首次确定后每次都要使用同一个路径。`RAY_REPOSITORY` 可在首次接续时指定 fork 仓库，默认是 `https://github.com/ljdzxx/sub2api.git`，分支固定为 `ray`。私有仓库需事先配置服务器 Git 凭据。

启动迁移超过默认 600 秒时，可设置 `RAY_WAIT_TIMEOUT=1200`。如果运维状态里的数据挂载、库身份校验失败，应检查真实变化，不要清库重装。

## 开发验证

```bash
bash -n deploy-ray/ops.sh
python3 -m unittest discover -s deploy-ray/tests -p 'test_ops.py' -v
```

测试覆盖旧容器已停止、连接与挂载核验、空库拒绝、容器启动检查、备份失败阻止替换、单实例更新范围，以及原应用数据卷复用；不连接真实生产数据库。
