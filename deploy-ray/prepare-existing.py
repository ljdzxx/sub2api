#!/usr/bin/env python3
"""Prepare a salve-only Compose file against existing production containers.

Only reads Docker metadata. Never starts, stops, connects or modifies containers.
The generated file contains secrets and is created exclusively with mode 0600.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys


DEPLOY_DIR = Path(__file__).resolve().parent


def make_config(containers, repo_dir, network=None, commit="ray-local"):
    app, postgres, redis = containers
    for container in containers:
        state = container.get("State", {})
        if not state.get("Running") or state.get("Health", {}).get("Status", "healthy") != "healthy":
            raise ValueError(f"Container {container.get('Name', '?')} must be running and healthy")
    shared = set.intersection(*(set(c.get("NetworkSettings", {}).get("Networks", {})) for c in containers))
    if network:
        if network not in shared:
            raise ValueError("The selected network must already connect all three containers")
    else:
        if len(shared) != 1:
            raise ValueError("Specify --network with one of the networks shared by all three containers: " + ", ".join(sorted(shared)))
        network = next(iter(shared))
    if network in {"bridge", "host", "none"}:
        raise ValueError("A user-defined Docker network with container DNS is required")

    environment = dict(item.split("=", 1) for item in app.get("Config", {}).get("Env", []) if "=" in item)
    for key in ("PATH", "HOME", "HOSTNAME", "PWD", "SHLVL", "SKIP_SETUP"):
        environment.pop(key, None)
    # Old docker-deploy.sh generates these fixed values. Do not silently generate
    # replacements if an installation instead stored its effective secrets in YAML.
    required = ("DATABASE_USER", "DATABASE_DBNAME", "DATABASE_PASSWORD", "JWT_SECRET", "TOTP_ENCRYPTION_KEY")
    missing = [key for key in required if not environment.get(key)]
    if missing:
        raise ValueError("Missing fixed settings in the running app environment: " + ", ".join(missing) + ". Check its /app/data/config.yaml before proceeding; no new secrets were generated.")

    environment.update({
        "AUTO_SETUP": "true", "SERVER_HOST": "0.0.0.0", "SERVER_PORT": "8080", "DATA_DIR": "/app/data",
        "DATABASE_HOST": postgres["Name"].lstrip("/"), "REDIS_HOST": redis["Name"].lstrip("/"),
        # Leave connection headroom alongside the existing production pool.
        "DATABASE_MAX_OPEN_CONNS": "20", "DATABASE_MAX_IDLE_CONNS": "5",
    })
    environment.setdefault("DATABASE_PORT", "5432")
    environment.setdefault("DATABASE_SSLMODE", "disable")
    environment.setdefault("REDIS_PORT", "6379")
    environment.setdefault("REDIS_DB", "0")
    environment.setdefault("REDIS_PASSWORD", "")

    # Compose interpolates $ even in JSON/YAML values. Escape it so secrets and
    # other settings survive literal $, ${...}, quotes, # and newline characters.
    environment = {key: value.replace("$", "$$") for key, value in environment.items()}
    return {
        "services": {
            "salve": {
                "image": "sub2api-ray:salve", "pull_policy": "never",
                "build": {"context": str(repo_dir), "dockerfile": "Dockerfile", "args": {"COMMIT": commit}},
                "hostname": "sub2api-ray-salve", "restart": "unless-stopped",
                "security_opt": ["no-new-privileges:true"], "stop_grace_period": "60s",
                "ports": ["0.0.0.0:8081:8080"], "environment": environment,
                "volumes": ["salve_data:/app/data"], "networks": ["production"],
                "ulimits": {"nofile": {"soft": 100000, "hard": 100000}},
                "healthcheck": {"test": ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "http://127.0.0.1:8080/health"],
                                "interval": "10s", "timeout": "6s", "retries": 18, "start_period": "90s"},
                "logging": {"driver": "json-file", "options": {"max-size": "10m", "max-file": "3"}},
            }
        },
        "networks": {"production": {"external": True, "name": network}},
        "volumes": {"salve_data": {}},
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--app-container", default="sub2api")
    parser.add_argument("--postgres-container", default="sub2api-postgres")
    parser.add_argument("--redis-container", default="sub2api-redis")
    parser.add_argument("--network", help="Choose a shared network if there are multiple")
    args = parser.parse_args()
    output = DEPLOY_DIR / "compose.existing.local.json"
    if output.exists():
        raise ValueError(f"{output.name} already exists; keep it or move it to a protected backup before regenerating")
    repo_dir = DEPLOY_DIR.parent
    if not (repo_dir / "Dockerfile").is_file():
        raise ValueError("Keep deploy-ray in the complete ray source checkout")
    result = subprocess.run(["docker", "inspect", args.app_container, args.postgres_container, args.redis_container],
                            capture_output=True, text=True, check=True)
    containers = json.loads(result.stdout)
    if len(containers) != 3:
        raise ValueError("Expected metadata for exactly three containers")
    try:
        commit = subprocess.check_output(["git", "-C", str(repo_dir), "rev-parse", "--short", "HEAD"], stderr=subprocess.DEVNULL, text=True).strip()
    except (OSError, subprocess.CalledProcessError):
        commit = "ray-local"
    config = make_config(containers, repo_dir, args.network, commit)
    # O_EXCL prevents accidental replacement of a previously prepared file.
    fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        json.dump(config, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
    print(f"Created {output.name} (contains secrets; mode 600).")
    print(f"Existing network: {config['networks']['production']['name']}")
    print("Only salve on port 8081 is defined. Existing containers were not modified.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as exc:
        # Never print docker inspect stdout (it contains production secrets).
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(1)
