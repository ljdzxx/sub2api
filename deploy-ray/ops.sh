#!/usr/bin/env bash
# Standalone existing-production operator. Download this ONE file and run bash ops.sh.
set -Eeuo pipefail
export RAY_OPS_SCRIPT_DIR
RAY_OPS_SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
command -v python3 >/dev/null 2>&1 || { echo '需要 Python 3，请先安装 python3。' >&2; exit 1; }

run_action() {
python3 - "$@" <<'RAY_OPS_PYTHON'
import argparse
import contextlib
import datetime
import json
import os
from pathlib import Path
import posixpath
import re
import shutil
import subprocess
import sys
import tempfile

REPOSITORY = os.environ.get("RAY_REPOSITORY", "https://github.com/ljdzxx/sub2api.git")
STATE_DIR = Path(os.environ.get("RAY_OPS_STATE_DIR", str(Path(os.environ.get("RAY_OPS_SCRIPT_DIR", ".")) / ".ray-ops"))).resolve()
ROLE_NAMES = ("master", "salve")
APP_NAMES = {role: "sub2api-ray-" + role for role in ROLE_NAMES}
CORE_QUERY = "SELECT current_database(), oid::text, (to_regclass('public.users') IS NOT NULL AND to_regclass('public.accounts') IS NOT NULL AND to_regclass('public.api_keys') IS NOT NULL AND to_regclass('public.settings') IS NOT NULL AND to_regclass('public.schema_migrations') IS NOT NULL)::int FROM pg_database WHERE datname=current_database();"
ROWS_QUERY = "SELECT (EXISTS (SELECT 1 FROM public.users) AND EXISTS (SELECT 1 FROM public.schema_migrations))::int;"

# Runs inside EVERY new application container, including Docker automatic restarts.
# Read-only psql probes run before the upstream entrypoint and before Go migrations.
GUARD = r'''#!/bin/sh
set -eu
[ "${SKIP_SETUP:-}" = true ] && [ "${AUTO_SETUP:-}" = false ] || {
  echo 'ray guard: installation must be disabled' >&2; exit 1;
}
probe() {
  PGHOST="$DATABASE_HOST" PGPORT="${DATABASE_PORT:-5432}" PGUSER="$DATABASE_USER" \
  PGPASSWORD="$DATABASE_PASSWORD" PGDATABASE="$DATABASE_DBNAME" \
  PGSSLMODE="${DATABASE_SSLMODE:-disable}" PGCONNECT_TIMEOUT=10 \
  PGOPTIONS='-c default_transaction_read_only=on' \
    psql -X -v ON_ERROR_STOP=1 -At -F '|' -c "$1"
}
identity=$(probe "__CORE_QUERY__")
[ "$identity" = "$DATABASE_DBNAME|$RAY_EXPECTED_DATABASE_OID|1" ] || {
  echo 'ray guard: original database identity/schema mismatch; refusing startup' >&2; exit 1;
}
[ "$(probe "__ROWS_QUERY__")" = 1 ] || {
  echo 'ray guard: empty database; refusing startup' >&2; exit 1;
}
# PGOPTIONS and PGPASSWORD above apply to psql only, never to the application.
exec /app/docker-entrypoint.sh "$@"
'''.replace("__CORE_QUERY__", CORE_QUERY).replace("__ROWS_QUERY__", ROWS_QUERY)


class OpsError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise OpsError(message)


def run(args, *, capture=False, env=None, stdout=None):
    # Never dump inspect/config output or child environment into an error message.
    try:
        result = subprocess.run(args, check=True, text=True, env=env,
                                stdout=subprocess.PIPE if capture else stdout,
                                stderr=subprocess.PIPE if capture else None)
        return result.stdout.strip() if capture else None
    except FileNotFoundError:
        raise OpsError("缺少命令：" + args[0]) from None
    except subprocess.CalledProcessError:
        raise OpsError("命令执行失败：" + " ".join(args[:3]) + "；已停止后续操作。") from None


def inspect(name, missing_ok=False):
    result = subprocess.run(["docker", "inspect", name], capture_output=True, text=True)
    if result.returncode:
        if missing_ok:
            return None
        raise OpsError("无法读取容器：" + name)
    return json.loads(result.stdout)[0]


def env_of(container):
    return dict(item.split("=", 1) for item in container.get("Config", {}).get("Env", []) if "=" in item)


def healthy(container):
    state = container.get("State", {})
    return state.get("Running") and state.get("Health", {}).get("Status", "healthy") == "healthy"


def mount_for(container, destination):
    matches = [m for m in container.get("Mounts", []) if destination == m["Destination"] or destination.startswith(m["Destination"].rstrip("/") + "/")]
    require(matches, "原容器的持久化挂载无法确认：" + container["Name"])
    return max(matches, key=lambda m: len(m["Destination"]))


def mount_identity(mount):
    return {key: mount.get(key, "") for key in ("Type", "Source", "Destination", "Name")}


def docker_host_names(container, network):
    settings = container["NetworkSettings"]["Networks"][network]
    return {container["Name"].lstrip("/"), settings.get("IPAddress", ""), *(settings.get("Aliases") or [])}


def discover(app, postgres, redis, network=None):
    # The old application may be stopped. The two existing shared services may not.
    require(healthy(postgres) and healthy(redis), "原 PostgreSQL/Redis 必须运行且健康；本脚本不会创建或启动它们。")
    shared = set.intersection(*(set(c.get("NetworkSettings", {}).get("Networks", {})) for c in (app, postgres, redis)))
    if network:
        require(network in shared, "指定网络未同时连接原应用、数据库和 Redis。")
    else:
        require(len(shared) == 1, "共同网络不唯一，请设置 RAY_EXISTING_NETWORK；候选：" + ", ".join(sorted(shared)))
        network = next(iter(shared))
    require(network not in ("bridge", "host", "none"), "需要原部署的自定义 Docker 网络。")
    environment = env_of(app)
    required = ("DATABASE_HOST", "DATABASE_PORT", "DATABASE_SSLMODE", "DATABASE_USER", "DATABASE_DBNAME", "DATABASE_PASSWORD", "REDIS_HOST", "REDIS_PORT", "REDIS_DB", "JWT_SECRET", "TOTP_ENCRYPTION_KEY")
    require(all(environment.get(key) for key in required), "原容器环境缺少数据库连接或固定 JWT/TOTP 密钥，请核实原配置；不会生成替代值。")
    require("REDIS_PASSWORD" in environment, "原 Redis 密码未显式提供（允许显式空密码），拒绝猜测原连接配置。")
    require(re.fullmatch(r"[A-Za-z0-9_.-]+", environment["DATABASE_DBNAME"]), "数据库名称需人工核实，拒绝将连接串作为库名。")
    require(environment["DATABASE_HOST"] in docker_host_names(postgres, network), "原 DATABASE_HOST 未指向所选 PostgreSQL 容器，拒绝自动改接其他数据库。")
    require(environment["REDIS_HOST"] in docker_host_names(redis, network), "原 REDIS_HOST 未指向所选 Redis 容器，拒绝自动改接其他 Redis。")
    pgdata = env_of(postgres).get("PGDATA", "/var/lib/postgresql/data")
    pg_mount = mount_identity(mount_for(postgres, pgdata))
    redis_mount = mount_identity(mount_for(redis, "/data"))
    data_dir = environment.get("DATA_DIR", "/app/data")
    app_mount = mount_for(app, data_dir)
    require(app_mount["Destination"] == data_dir and app_mount["Type"] in ("bind", "volume"), "原应用 DATA_DIR 必须有独立 bind/volume 挂载，请人工核实。")
    require(app_mount.get("RW", True), "原应用数据目录不可写。")
    app_source = app_mount["Source"].rstrip("/")
    for shared_mount in (pg_mount, redis_mount):
        shared_source = shared_mount["Source"].rstrip("/")
        require(app_source != shared_source and not app_source.startswith(shared_source + "/") and not shared_source.startswith(app_source + "/"), "应用数据挂载与数据库/Redis 存储重叠，拒绝操作。")
    for key in ("PATH", "HOME", "HOSTNAME", "PWD", "SHLVL", "ADMIN_EMAIL", "ADMIN_PASSWORD"):
        environment.pop(key, None)
    environment.update({"SKIP_SETUP": "true", "AUTO_SETUP": "false", "SERVER_HOST": "0.0.0.0", "SERVER_PORT": "8080", "DATA_DIR": "/app/data"})
    # Keep the original verified DNS aliases, including TLS hostname semantics.
    environment.setdefault("DATABASE_PORT", "5432")
    environment.setdefault("DATABASE_SSLMODE", "disable")
    environment.setdefault("REDIS_PORT", "6379")
    environment.setdefault("REDIS_DB", "0")
    environment.setdefault("REDIS_PASSWORD", "")
    environment["DATABASE_MAX_OPEN_CONNS"] = "20"
    environment["DATABASE_MAX_IDLE_CONNS"] = "5"
    return {"version": 1, "old_app": app["Name"].lstrip("/"), "old_image": app["Image"],
            "postgres": postgres["Name"].lstrip("/"), "redis": redis["Name"].lstrip("/"),
            "postgres_mount": pg_mount, "redis_mount": redis_mount, "pgdata": pgdata,
            "app_mount": {**mount_identity(app_mount), "Mode": app_mount.get("Mode", "")}, "old_data_dir": data_dir,
            "network": network, "environment": environment, "images": {}, "repository": REPOSITORY}


def pg_env(state):
    source = state["environment"]
    return {**os.environ, "PGHOST": source["DATABASE_HOST"], "PGPORT": source["DATABASE_PORT"],
            "PGUSER": source["DATABASE_USER"], "PGPASSWORD": source["DATABASE_PASSWORD"],
            "PGDATABASE": source["DATABASE_DBNAME"], "PGSSLMODE": source["DATABASE_SSLMODE"],
            "PGCONNECT_TIMEOUT": "10", "PGOPTIONS": "-c default_transaction_read_only=on"}


def pg_command(state, command):
    args = ["docker", "exec"]
    # Values are inherited from the docker CLI environment, not exposed in argv.
    for name in ("PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE", "PGSSLMODE", "PGCONNECT_TIMEOUT", "PGOPTIONS"):
        args += ["-e", name]
    return args + [state["postgres"]] + command


def probe_db(state):
    identity = run(pg_command(state, ["psql", "-X", "-v", "ON_ERROR_STOP=1", "-At", "-F", "|", "-c", CORE_QUERY]), capture=True, env=pg_env(state))
    fields = identity.split("|")
    require(len(fields) == 3 and fields[0] == state["environment"]["DATABASE_DBNAME"] and fields[2] == "1" and fields[1].isdigit(), "目标库不是完整的原业务库，拒绝启动；不会创建数据库或初始化空库。")
    if "database_oid" in state:
        require(fields[1] == state["database_oid"], "数据库标识已变化，拒绝接续到替换过的数据库。")
    rows = run(pg_command(state, ["psql", "-X", "-v", "ON_ERROR_STOP=1", "-At", "-c", ROWS_QUERY]), capture=True, env=pg_env(state))
    require(rows == "1", "目标库没有原用户数据或迁移记录，拒绝初始化空库。")
    return fields[1]


def verify(state):
    postgres, redis = inspect(state["postgres"]), inspect(state["redis"])
    require(healthy(postgres) and healthy(redis), "原 PostgreSQL/Redis 未运行或不健康，操作终止；不会自动新建或重启。")
    require(env_of(postgres).get("PGDATA", "/var/lib/postgresql/data") == state["pgdata"], "PostgreSQL PGDATA 已变化，拒绝继续。")
    require(mount_identity(mount_for(postgres, state["pgdata"])) == state["postgres_mount"], "PostgreSQL 数据挂载已变化，拒绝继续。")
    require(mount_identity(mount_for(redis, "/data")) == state["redis_mount"], "Redis 数据挂载已变化，拒绝继续。")
    require(all(state["network"] in c["NetworkSettings"]["Networks"] for c in (postgres, redis)), "原共享网络已变化，拒绝继续。")
    probe_db(state)


def private_write(path, value, mode=0o600):
    fd, temporary = tempfile.mkstemp(prefix=".tmp-", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(value)
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def save(state):
    private_write(STATE_DIR / "connection.json", json.dumps(state, ensure_ascii=False, indent=2) + "\n")


def load_or_attach():
    filename = STATE_DIR / "connection.json"
    if filename.exists():
        state = json.loads(filename.read_text(encoding="utf-8"))
        require(state.get("version") == 1, "运维状态版本不兼容；保留原文件，不自动重置。")
        return state
    names = [os.environ.get("RAY_OLD_APP", "sub2api"), os.environ.get("RAY_POSTGRES", "sub2api-postgres"), os.environ.get("RAY_REDIS", "sub2api-redis")]
    state = discover(*(inspect(name) for name in names), network=os.environ.get("RAY_EXISTING_NETWORK"))
    state["database_oid"] = probe_db(state)
    # Preserve the original YAML for salve, including settings not present in Env.
    config = STATE_DIR / "original-config.yaml"
    run(["docker", "cp", state["old_app"] + ":" + posixpath.join(state["old_data_dir"], "config.yaml"), str(config)])
    os.chmod(config, 0o600)
    private_write(STATE_DIR / "existing-db-guard.sh", GUARD, 0o644)
    save(state)
    print("已接续原业务库：" + state["environment"]["DATABASE_DBNAME"] + "；未新建或初始化数据库。", flush=True)
    return state


def compose_model(state, role, image, directory=None):
    directory = directory or STATE_DIR
    env = {**state["environment"], "RAY_EXPECTED_DATABASE_OID": state["database_oid"]}
    env = {key: str(value) for key, value in env.items()}
    volumes = {}
    if role == "master":
        original = state["app_mount"]
        if original["Type"] == "bind":
            data_mount = {"type": "bind", "source": original["Source"], "target": "/app/data", "bind": {"create_host_path": False}}
            for label in ("z", "Z"):
                if label in original.get("Mode", "").split(","):
                    data_mount["bind"]["selinux"] = label
        else:
            data_mount = {"type": "volume", "source": "original_app_data", "target": "/app/data"}
            volumes["original_app_data"] = {"external": True, "name": original["Name"]}
    else:
        data_mount = {"type": "bind", "source": str(directory / "salve-data"), "target": "/app/data", "bind": {"create_host_path": False, "selinux": "Z"}}
    config = {
        "services": {role: {
            "image": image, "pull_policy": "never", "container_name": APP_NAMES[role], "hostname": APP_NAMES[role],
            "restart": "unless-stopped", "stop_grace_period": "60s", "security_opt": ["no-new-privileges:true"],
            "environment": env, "ports": ["0.0.0.0:" + ("8080" if role == "master" else "8081") + ":8080"],
            "networks": ["original"], "volumes": [data_mount, {"type": "bind", "source": str(directory / "existing-db-guard.sh"),
                "target": "/app/ray-existing-db-guard.sh", "read_only": True, "bind": {"create_host_path": False, "selinux": "z"}}],
            "entrypoint": ["/bin/sh", "/app/ray-existing-db-guard.sh"], "command": ["/app/sub2api"],
            "ulimits": {"nofile": {"soft": 100000, "hard": 100000}},
            "healthcheck": {"test": ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "http://127.0.0.1:8080/health"],
                "interval": "10s", "timeout": "6s", "retries": 18, "start_period": "90s"},
            "logging": {"driver": "json-file", "options": {"max-size": "10m", "max-file": "3"}},
        }}, "networks": {"original": {"external": True, "name": state["network"]}},
    }
    if volumes:
        config["volumes"] = volumes
    def literal(value):
        if isinstance(value, str):
            return value.replace("$", "$$")
        if isinstance(value, dict):
            return {key: literal(item) for key, item in value.items()}
        if isinstance(value, list):
            return [literal(item) for item in value]
        return value
    return literal(config)


def render(state, role, image):
    if role == "salve":
        data = STATE_DIR / "salve-data"
        data.mkdir(exist_ok=True)
        config = data / "config.yaml"
        if not config.exists():
            shutil.copyfile(STATE_DIR / "original-config.yaml", config)
            os.chmod(config, 0o600)
    private_write(STATE_DIR / "existing-db-guard.sh", GUARD, 0o644)
    path = STATE_DIR / (role + ".compose.json")
    private_write(path, json.dumps(compose_model(state, role, image), ensure_ascii=False, indent=2) + "\n")
    return path


def compose(role, *args):
    path = STATE_DIR / (role + ".compose.json")
    require(path.exists(), "此实例尚未部署：" + role)
    return run(["docker", "compose", "--project-name", "sub2api-ray-" + role, "-f", str(path), *args])


def check_old_stopped(state):
    old = inspect(state["old_app"], missing_ok=True)
    require(old is None or not old.get("State", {}).get("Running"), "请先停止旧应用：docker stop " + state["old_app"] + "；保留 PostgreSQL/Redis 运行。")


def sync_build(state):
    require(shutil.which("git"), "需要安装 git。")
    print("获取 ray 源码并构建应用镜像……", flush=True)
    source = STATE_DIR / "source"
    if not source.exists():
        run(["git", "clone", "--branch", "ray", "--single-branch", state["repository"], str(source)])
    else:
        require((source / ".git").exists(), "受管源码目录不是 Git 仓库，拒绝覆盖。")
        require(run(["git", "-C", str(source), "status", "--porcelain"], capture=True) == "", "受管源码有本地修改，请先处理；不会强制覆盖。")
        require(run(["git", "-C", str(source), "branch", "--show-current"], capture=True) == "ray", "受管源码必须处于 ray 分支。")
        run(["git", "-C", str(source), "fetch", "origin", "ray"])
        run(["git", "-C", str(source), "merge", "--ff-only", "origin/ray"])
    commit = run(["git", "-C", str(source), "rev-parse", "HEAD"], capture=True)
    image = "sub2api-ray:" + commit[:12]
    run(["docker", "build", "--build-arg", "COMMIT=" + commit, "-t", image, "-f", str(source / "Dockerfile"), str(source)])
    return image


def backup(state):
    verify(state)
    print("正在只读备份原数据库……", flush=True)
    folder = STATE_DIR / "backups"
    folder.mkdir(exist_ok=True)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    fd, name = tempfile.mkstemp(prefix=stamp + "-", suffix=".dump.partial", dir=folder)
    partial = Path(name)
    final = partial.with_suffix("")
    try:
        with os.fdopen(fd, "wb") as output:
            run(pg_command(state, ["pg_dump", "-Fc"]), env=pg_env(state), stdout=output)
        require(partial.stat().st_size > 0, "数据库备份为空，取消更新。")
        with partial.open("rb") as data:
            result = subprocess.run(["docker", "exec", "-i", state["postgres"], "pg_restore", "--list"], stdin=data, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        require(result.returncode == 0, "数据库备份校验失败，取消更新。")
        os.replace(partial, final)
        print("只读数据库备份已保存：" + str(final), flush=True)
        return final
    finally:
        if partial.exists():
            partial.unlink()


def image_present(image):
    return bool(image) and subprocess.run(["docker", "image", "inspect", image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def selected_roles(target):
    require(target in (*ROLE_NAMES, "all"), "目标只能是 master、salve 或 all。")
    return ROLE_NAMES if target == "all" else (target,)


def change_apps(state, action, roles, backup_before_change=True):
    check_old_stopped(state)
    verify(state)
    # Build first. A failed fetch/build/backup never stops an existing managed app.
    built = sync_build(state) if action == "update" or any(not image_present(state["images"].get(r)) for r in roles) else None
    if backup_before_change:
        backup(state)
    else:
        print("本次已选择不备份；原库身份和启动检查仍然生效。", flush=True)
    for role in roles:
        verify(state)
        desired = built if action == "update" or not image_present(state["images"].get(role)) else state["images"][role]
        render(state, role, desired)
        compose(role, "config", "--quiet")
        previous = state["images"].get(role)
        # Persist the intended version for diagnosis even if health checks fail.
        state.setdefault("previous_images", {})[role] = previous
        state["images"][role] = desired
        save(state)
        recreate = ["--force-recreate"] if action == "update" else []
        compose(role, "up", "-d", "--no-deps", "--no-build", "--pull", "never", *recreate, "--wait", "--wait-timeout", os.environ.get("RAY_WAIT_TIMEOUT", "600"), role)
        print(role + " 已启动，端口 " + ("8080" if role == "master" else "8081"), flush=True)


@contextlib.contextmanager
def operation_lock():
    # Linux advisory file lock is released automatically even after interruption.
    import fcntl
    with (STATE_DIR / "operator.lock").open("a") as handle:
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise OpsError("另一个运维操作正在运行，请等待完成。") from None
        yield


def dispatch(action, target, backup_before_change=True):
    require(shutil.which("docker"), "需要安装 Docker。")
    run(["docker", "compose", "version"], capture=True)
    run(["docker", "info"], capture=True)
    state = load_or_attach()
    if action == "attach":
        verify(state)
        print("原库核验通过。")
    elif action in ("start", "update"):
        change_apps(state, action, selected_roles(target), backup_before_change=backup_before_change)
    elif action == "stop":
        for role in selected_roles(target):
            if (STATE_DIR / (role + ".compose.json")).exists():
                compose(role, "stop", role)
            else:
                print(role + " 尚未部署，无需停止。")
    elif action == "restart":
        check_old_stopped(state)
        verify(state)
        for role in selected_roles(target):
            compose(role, "restart", role)
    elif action == "logs":
        require(target in ROLE_NAMES, "日志目标只能是 master 或 salve。")
        compose(target, "logs", "-f", "--tail", "100", target)
    elif action == "backup":
        backup(state)
    elif action == "status":
        for name in (state["old_app"], state["postgres"], state["redis"], *APP_NAMES.values()):
            container = inspect(name, missing_ok=True)
            if container:
                info = container.get("State", {})
                print(name + ": " + info.get("Status", "unknown") + " " + info.get("Health", {}).get("Status", ""))
            else:
                print(name + ": 未创建")
        print("原库：" + state["environment"]["DATABASE_DBNAME"] + "；网络：" + state["network"])


def main():
    parser = argparse.ArgumentParser(description="接续原数据库的一键运维；不创建 PostgreSQL/Redis，不初始化空库。")
    parser.add_argument("action", choices=("attach", "start", "stop", "restart", "update", "logs", "status", "backup"))
    parser.add_argument("target", nargs="?", default="all")
    backup_options = parser.add_mutually_exclusive_group()
    backup_options.add_argument("--backup", dest="backup_mode", action="store_true", help="启动/更新前备份（命令行默认）")
    backup_options.add_argument("--no-backup", dest="backup_mode", action="store_false", help="本次启动/更新不备份；仍核验原库")
    parser.set_defaults(backup_mode=None)
    args = parser.parse_args()
    require(args.backup_mode is None or args.action in ("start", "update"), "--backup/--no-backup 仅用于 start 或 update。")
    require(re.fullmatch(r"[1-9][0-9]*", os.environ.get("RAY_WAIT_TIMEOUT", "600")), "RAY_WAIT_TIMEOUT 必须为正整数秒。")
    if args.action in ("start", "stop", "restart", "update"):
        selected_roles(args.target)
    if args.action == "logs":
        require(args.target in ROLE_NAMES, "日志目标只能是 master 或 salve。")
    os.umask(0o077)
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    os.chmod(STATE_DIR, 0o700)
    if args.action in ("logs", "status"):
        if not (STATE_DIR / "connection.json").exists():
            with operation_lock():
                load_or_attach()
        # Following logs must not block start/stop/update from another terminal.
        dispatch(args.action, args.target, backup_before_change=args.backup_mode is not False)
    else:
        with operation_lock():
            dispatch(args.action, args.target, backup_before_change=args.backup_mode is not False)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("操作已中断；没有自动回退镜像或恢复数据库。", file=sys.stderr)
        sys.exit(130)
    except (OpsError, OSError, ValueError, KeyError) as error:
        print("操作终止：" + str(error), file=sys.stderr)
        sys.exit(1)
RAY_OPS_PYTHON
}

choose_backup_and_run() {
  local answer
  read -r -p '本次启动/更新前备份数据库？[Y/n]：' answer || return 1
  case "$answer" in
    ''|y|Y|yes|YES|Yes) run_action "$@" --backup ;;
    n|N|no|NO|No) run_action "$@" --no-backup ;;
    *) echo '请输入 Y 或 n，本次操作未执行。' >&2; return 1 ;;
  esac
}

if [[ $# -gt 0 ]]; then
  run_action "$@"
  exit $?
fi

while true; do
  cat <<'MENU'

==== ray 运维：仅接续原数据库 ====
1) 启动 master :8080    2) 启动 salve :8081    3) 启动两个实例
4) 停止 master          5) 停止 salve          6) 停止两个实例
7) 更新 master          8) 更新 salve          9) 更新两个实例
10) master 日志         11) salve 日志         12) 查看状态
13) 只读备份数据库      14) 核验原库接续       0) 退出
MENU
  read -r -p '请选择：' choice || exit 0
  case "$choice" in
    1) choose_backup_and_run start master || true ;;
    2) choose_backup_and_run start salve || true ;;
    3) choose_backup_and_run start all || true ;;
    4) run_action stop master || true ;;
    5) run_action stop salve || true ;;
    6) run_action stop all || true ;;
    7) choose_backup_and_run update master || true ;;
    8) choose_backup_and_run update salve || true ;;
    9) choose_backup_and_run update all || true ;;
    10) run_action logs master || true ;;
    11) run_action logs salve || true ;;
    12) run_action status || true ;;
    13) run_action backup || true ;;
    14) run_action attach || true ;;
    0) exit 0 ;;
    *) echo '无效选项。' ;;
  esac
done
