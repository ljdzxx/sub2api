import copy
import contextlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import types
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "ops.sh"
PYTHON_SOURCE = SCRIPT.read_text(encoding="utf-8").split("<<'RAY_OPS_PYTHON'\n", 1)[1].split("\nRAY_OPS_PYTHON\n", 1)[0]
ops = types.ModuleType("ray_ops_test")
exec(compile(PYTHON_SOURCE, str(SCRIPT), "exec"), ops.__dict__)
BASH = r"C:\Program Files\Git\bin\bash.exe" if os.name == "nt" else shutil.which("bash")


def containers():
    result = []
    for name, alias, destination, source in (
        ("sub2api", "sub2api", "/app/data", "/srv/production/data"),
        ("sub2api-postgres", "postgres", "/var/lib/postgresql/data", "/srv/production/postgres_data"),
        ("sub2api-redis", "redis", "/data", "/srv/production/redis_data"),
    ):
        result.append({"Name": "/" + name, "Image": "sha256:old", "State": {"Running": True, "Health": {"Status": "healthy"}},
            "NetworkSettings": {"Networks": {"production_network": {"Aliases": [alias, name], "IPAddress": "172.20.0.2"}}},
            "Mounts": [{"Type": "bind", "Source": source, "Destination": destination, "RW": True, "Mode": "Z"}], "Config": {"Env": []}})
    result[0]["State"]["Running"] = False
    result[0]["Config"]["Env"] = [
        "DATABASE_HOST=postgres", "DATABASE_PORT=5432", "DATABASE_SSLMODE=disable", "DATABASE_USER=prod_user",
        "DATABASE_PASSWORD=pw$with${literal}", "DATABASE_DBNAME=production", "REDIS_HOST=redis", "REDIS_PORT=6379",
        "REDIS_DB=2", "REDIS_PASSWORD=", "JWT_SECRET=" + "a" * 64, "TOTP_ENCRYPTION_KEY=" + "b" * 64,
        "AUTO_SETUP=true", "ADMIN_PASSWORD=old", "GATEWAY_FORCE_CODEX_CLI=true",
    ]
    result[1]["Config"]["Env"] = ["PGDATA=/var/lib/postgresql/data"]
    return result


def state():
    result = ops.discover(*containers())
    result["database_oid"] = "16384"
    return result


class OpsTests(unittest.TestCase):
    def test_stopped_old_app_is_supported_and_shared_secrets_are_reused(self):
        source = containers()
        before = copy.deepcopy(source)
        s = ops.discover(*source)
        self.assertEqual(source, before)
        self.assertEqual(s["environment"]["SKIP_SETUP"], "true")
        self.assertEqual(s["environment"]["AUTO_SETUP"], "false")
        self.assertEqual(s["environment"]["DATABASE_DBNAME"], "production")
        self.assertEqual(s["environment"]["JWT_SECRET"], "a" * 64)
        self.assertEqual(s["environment"]["TOTP_ENCRYPTION_KEY"], "b" * 64)
        self.assertEqual(s["environment"]["REDIS_DB"], "2")
        self.assertEqual(s["environment"]["REDIS_PASSWORD"], "")
        self.assertNotIn("ADMIN_PASSWORD", s["environment"])
        self.assertEqual(s["app_mount"]["Source"], "/srv/production/data")

    def test_unrelated_database_host_is_not_silently_replaced(self):
        source = containers()
        source[0]["Config"]["Env"][0] = "DATABASE_HOST=external-db.example.com"
        with self.assertRaisesRegex(ops.OpsError, "DATABASE_HOST"):
            ops.discover(*source)

    def test_app_mount_cannot_overlap_database_storage(self):
        source = containers()
        source[0]["Mounts"][0]["Source"] = "/srv/production"
        with self.assertRaisesRegex(ops.OpsError, "重叠"):
            ops.discover(*source)

    def test_empty_database_or_changed_oid_is_rejected_read_only(self):
        for outputs in (["production|16384|0"], ["production|99999|1"], ["production|16384|1", "0"]):
            with self.subTest(outputs=outputs), patch.object(ops, "run", side_effect=outputs) as run:
                with self.assertRaises(ops.OpsError):
                    ops.probe_db(state())
                for call in run.call_args_list:
                    self.assertEqual(call.kwargs["env"]["PGOPTIONS"], "-c default_transaction_read_only=on")
                    self.assertNotIn("pw$with${literal}", call.args[0])

    def test_valid_database_identity_is_preserved(self):
        with patch.object(ops, "run", side_effect=["production|16384|1", "1"]):
            self.assertEqual(ops.probe_db(state()), "16384")

    def test_changed_postgres_mount_blocks_start(self):
        source = containers()
        s = state()
        source[1]["Mounts"][0]["Source"] = "/empty/new/postgres"
        with patch.object(ops, "inspect", side_effect=source[1:]), patch.object(ops, "probe_db") as probe:
            with self.assertRaisesRegex(ops.OpsError, "挂载"):
                ops.verify(s)
            probe.assert_not_called()

    def test_compose_only_defines_selected_app_and_external_network(self):
        for role, port in (("master", "8080"), ("salve", "8081")):
            model = ops.compose_model(state(), role, "sub2api-ray:abc", Path("/opt/ray-state"))
            self.assertEqual(set(model["services"]), {role})
            app = model["services"][role]
            self.assertEqual(app["ports"], ["0.0.0.0:" + port + ":8080"])
            self.assertNotIn("depends_on", app)
            self.assertEqual(model["networks"]["original"], {"external": True, "name": "production_network"})
            self.assertEqual(app["entrypoint"], ["/bin/sh", "/app/ray-existing-db-guard.sh"])
            self.assertEqual(app["environment"]["SKIP_SETUP"], "true")
            self.assertEqual(app["environment"]["AUTO_SETUP"], "false")
            self.assertEqual(app["environment"]["DATABASE_PASSWORD"], "pw$$with$${literal}")
            self.assertEqual(app["environment"]["RAY_EXPECTED_DATABASE_OID"], "16384")
            self.assertFalse(app["volumes"][0]["bind"]["create_host_path"])
            expected = "/srv/production/data" if role == "master" else str(Path("/opt/ray-state/salve-data"))
            self.assertEqual(app["volumes"][0]["source"], expected)

    def test_original_named_app_volume_is_external_not_new(self):
        s = state()
        s["app_mount"] = {"Type": "volume", "Name": "production_app_data"}
        model = ops.compose_model(s, "master", "sub2api-ray:abc")
        self.assertEqual(model["volumes"], {"original_app_data": {"external": True, "name": "production_app_data"}})

    def test_failed_backup_never_replaces_app(self):
        with patch.object(ops, "check_old_stopped"), patch.object(ops, "verify"), \
             patch.object(ops, "sync_build", return_value="new:image"), \
             patch.object(ops, "backup", side_effect=ops.OpsError("backup failed")), \
             patch.object(ops, "compose") as compose, patch.object(ops, "render") as render:
            with self.assertRaises(ops.OpsError):
                ops.change_apps(state(), "update", ("salve",))
            compose.assert_not_called()
            render.assert_not_called()

    def test_update_only_replaces_selected_role_after_backup(self):
        events = []
        with patch.object(ops, "check_old_stopped"), patch.object(ops, "verify"), \
             patch.object(ops, "sync_build", side_effect=lambda _: events.append("build") or "new:image"), \
             patch.object(ops, "backup", side_effect=lambda _: events.append("backup")), \
             patch.object(ops, "render"), patch.object(ops, "save"), \
             patch.object(ops, "compose", side_effect=lambda role, *args: events.append((role, args))):
            ops.change_apps(state(), "update", ("salve",))
        self.assertEqual(events[:2], ["build", "backup"])
        self.assertTrue(all(item[0] == "salve" for item in events[2:]))
        self.assertIn("--no-deps", events[-1][1])
        self.assertNotIn("master", events[-1][1])

    def test_skip_backup_does_not_skip_original_database_verification(self):
        with patch.object(ops, "check_old_stopped") as old, patch.object(ops, "verify") as verify, \
             patch.object(ops, "sync_build", return_value="new:image"), patch.object(ops, "backup") as backup, \
             patch.object(ops, "render"), patch.object(ops, "save"), patch.object(ops, "compose") as compose:
            ops.change_apps(state(), "update", ("salve",), backup_before_change=False)
        backup.assert_not_called()
        old.assert_called_once()
        self.assertGreaterEqual(verify.call_count, 2)
        self.assertIn("up", compose.call_args.args)

    def test_cli_backup_choice_reaches_dispatch(self):
        with tempfile.TemporaryDirectory(prefix="ray-backup-options-") as directory:
            root = Path(directory).resolve()
            self.assertEqual(root.parent, Path(tempfile.gettempdir()).resolve())
            for flags, expected in (([], True), (["--backup"], True), (["--no-backup"], False)):
                with patch.object(ops, "STATE_DIR", root), patch.object(ops.sys, "argv", ["ops.sh", "update", "salve", *flags]), \
                     patch.object(ops, "operation_lock", return_value=contextlib.nullcontext()), patch.object(ops, "dispatch") as dispatch:
                    ops.main()
                    dispatch.assert_called_once_with("update", "salve", backup_before_change=expected)
        with patch.object(ops.sys, "argv", ["ops.sh", "stop", "salve", "--no-backup"]), patch.object(ops, "dispatch") as dispatch:
            with self.assertRaisesRegex(ops.OpsError, "start"):
                ops.main()
            dispatch.assert_not_called()

    def test_backup_file_is_written_to_host_state_directory(self):
        with tempfile.TemporaryDirectory(prefix="ray-backup-location-") as directory:
            root = Path(directory).resolve()
            self.assertEqual(root.parent, Path(tempfile.gettempdir()).resolve())
            def dump(*args, **kwargs):
                kwargs["stdout"].write(b"PGDMP test archive")
            with patch.object(ops, "STATE_DIR", root), patch.object(ops, "verify"), patch.object(ops, "run", side_effect=dump), \
                 patch.object(ops.subprocess, "run", return_value=types.SimpleNamespace(returncode=0)) as validate:
                saved = ops.backup(state())
            self.assertEqual(saved.parent, root / "backups")
            self.assertEqual(saved.suffix, ".dump")
            self.assertTrue(saved.read_bytes().startswith(b"PGDMP"))
            self.assertEqual(list((root / "backups").glob("*.partial")), [])
            self.assertEqual(validate.call_args.args[0][-2:], ["pg_restore", "--list"])

    @unittest.skipUnless(BASH and Path(BASH).exists(), "Bash required")
    def test_menu_backup_choice_is_passed_without_running_docker(self):
        text = SCRIPT.read_text(encoding="utf-8")
        helper = text.split("choose_backup_and_run() {", 1)[1].split("\nif [[ $# -gt 0 ]]", 1)[0]
        shell = 'run_action() { printf "%s\\n" "$*"; }\nchoose_backup_and_run() {' + helper + '\nchoose_backup_and_run update salve\n'
        for answer, flag in (("\n", "--backup"), ("Y\n", "--backup"), ("n\n", "--no-backup")):
            # Send Linux terminal newlines unchanged, including when run on Windows.
            result = subprocess.run([BASH, "-c", shell], input=answer.encode(), capture_output=True, timeout=20)
            self.assertEqual(result.returncode, 0, result.stderr.decode("utf-8"))
            self.assertEqual(result.stdout.decode("utf-8").strip(), "update salve " + flag)

    def test_start_all_does_not_upgrade_an_existing_role_when_other_image_is_missing(self):
        s = state()
        s["images"]["master"] = "old:master"
        with patch.object(ops, "check_old_stopped"), patch.object(ops, "verify"), \
             patch.object(ops, "sync_build", return_value="new:salve"), patch.object(ops, "backup"), \
             patch.object(ops, "image_present", side_effect=lambda image: image == "old:master"), \
             patch.object(ops, "render") as render, patch.object(ops, "save"), patch.object(ops, "compose"):
            ops.change_apps(s, "start", ("master", "salve"))
        self.assertEqual([(call.args[1], call.args[2]) for call in render.call_args_list], [("master", "old:master"), ("salve", "new:salve")])

    @unittest.skipUnless(BASH and Path(BASH).exists(), "Bash required")
    def test_container_guard_blocks_empty_or_wrong_database_on_every_start(self):
        with tempfile.TemporaryDirectory(prefix="ray-guard-test-") as directory:
            root = Path(directory).resolve()
            self.assertEqual(root.parent, Path(tempfile.gettempdir()).resolve())
            binary = root / "bin"
            binary.mkdir()
            psql = binary / "psql"
            psql.write_text('''#!/usr/bin/env bash
set -eu
[[ "$PGOPTIONS" == '-c default_transaction_read_only=on' ]]
[[ "$PGDATABASE" == production ]]
if [[ "${!#}" == *"current_database()"* ]]; then
  [[ "${!#}" == *"to_regclass('public.users')"* ]]
  printf '%s\\n' "$TEST_IDENTITY"
else
  printf '%s\\n' "$TEST_ROWS"
fi
''', encoding="utf-8", newline="\n")
            psql.chmod(0o755)
            guard = root / "guard.sh"
            guard.write_text(ops.GUARD.replace('exec /app/docker-entrypoint.sh "$@"',
                             '[ "${PGOPTIONS:-}" = "" ]\nprintf "APP_STARTED\\n"'), encoding="utf-8", newline="\n")
            env = {**os.environ, **state()["environment"], "RAY_EXPECTED_DATABASE_OID": "16384"}
            for identity, rows, allowed in (("production|16384|1", "1", True), ("production|999|1", "1", False),
                                           ("production|16384|0", "1", False), ("production|16384|1", "0", False)):
                result = subprocess.run([BASH, "-c", 'export PATH="$(cd "$1" && pwd):$PATH"; exec sh "$2"', "guard",
                                         binary.as_posix(), guard.as_posix()], text=True, capture_output=True,
                                        env={**env, "TEST_IDENTITY": identity, "TEST_ROWS": rows}, timeout=20)
                self.assertEqual(result.returncode == 0, allowed, result.stdout + result.stderr)
                self.assertEqual("APP_STARTED" in result.stdout, allowed)


if __name__ == "__main__":
    unittest.main()
