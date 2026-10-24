"""Exercise deployment commands without a Docker daemon or real containers."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SOURCE = Path(__file__).resolve().parents[1]
BASH = (
    r"C:\Program Files\Git\bin\bash.exe" if os.name == "nt" else shutil.which("bash")
)

DOCKER_STUB = r'''#!/usr/bin/env bash
set -eu
printf '%s\t' "$@" >> "$DOCKER_TRACE"
printf '\n' >> "$DOCKER_TRACE"
case "$*" in
  'compose version') exit 0 ;;
  'image inspect '*) exit "${MOCK_IMAGE_MISSING:-0}" ;;
  'inspect '*) printf '%s\n' "${MOCK_INFRA_HEALTH:-true healthy}"; exit 0 ;;
  *' ps -q ray-postgres'|*' ps -q ray-redis')
    if [[ ${MOCK_MISSING_INFRA:-0} == 0 ]]; then printf 'infra-container\n'; fi
    exit 0 ;;
  *' config --images master') printf 'sub2api-ray:master\n'; exit 0 ;;
  *' config --images salve') printf 'sub2api-ray:salve\n'; exit 0 ;;
  *' exec -T ray-postgres '*)
    if [[ ${MOCK_BACKUP_FAIL:-0} == 1 ]]; then exit 1; fi
    printf 'PGDMP test backup\n'; exit 0 ;;
esac
if [[ "$*" == *' up '* && -n ${MOCK_UP_FAIL_FOR:-} && "${!#}" == "$MOCK_UP_FAIL_FOR" ]]; then
  exit 1
fi
exit 0
'''


@unittest.skipUnless(BASH and Path(BASH).exists(), "Bash is required")
class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="sub2api ray test ")
        self.root = Path(self.temp.name).resolve()
        # Cleanup is confined to this explicitly created temporary directory.
        self.assertEqual(self.root.parent, Path(tempfile.gettempdir()).resolve())
        self.addCleanup(self.temp.cleanup)
        self.deploy = self.root / "deploy-ray"
        shutil.copytree(SOURCE, self.deploy, ignore=shutil.ignore_patterns(".env", "backups", "__pycache__"))
        (self.root / "Dockerfile").write_text("FROM scratch\n", encoding="utf-8")
        self.bin = self.root / "bin"
        self.bin.mkdir()
        docker = self.bin / "docker"
        docker.write_text(DOCKER_STUB, encoding="utf-8", newline="\n")
        docker.chmod(0o755)
        self.trace = self.root / "docker-trace.txt"
        self.env = os.environ.copy()
        self.env["DOCKER_TRACE"] = self.trace.as_posix()
        self.env_file = self.deploy / ".env"
        self.env_file.write_text("POSTGRES_PASSWORD=test-only\n", encoding="utf-8")

    def run_script(self, *args, success=True, **env):
        result = subprocess.run(
            [BASH, "-c", 'export PATH="$(cd "$1" && pwd):$PATH"; shift; exec bash "$@"',
             "test", self.bin.as_posix(), (self.deploy / "deploy.sh").as_posix(), *args],
            env={**self.env, **env}, cwd=self.root, text=True, capture_output=True,
            encoding="utf-8", errors="replace", timeout=45,
        )
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def calls(self):
        if not self.trace.exists():
            return []
        return [line.rstrip("\t").split("\t") for line in self.trace.read_text(encoding="utf-8").splitlines()]

    def mutations(self):
        return [args for args in self.calls() if any(x in args for x in ("up", "stop", "restart", "build", "tag", "exec", "down"))]

    def test_each_app_can_start_without_starting_other_app_or_infrastructure(self):
        for role in ("master", "salve"):
            self.trace.write_text("", encoding="utf-8")
            self.run_script("up", role)
            mutations = self.mutations()
            self.assertEqual(len(mutations), 1)
            self.assertIn("sub2api-ray-" + role, mutations[0])
            self.assertEqual(mutations[0][-1], role)
            for option in ("--no-deps", "--no-build", "--wait"):
                self.assertIn(option, mutations[0])

    def test_single_app_stop_and_restart_are_isolated(self):
        for command in ("stop", "restart"):
            for role in ("master", "salve"):
                self.trace.write_text("", encoding="utf-8")
                self.run_script(command, role)
                mutations = self.mutations()
                self.assertEqual(len(mutations), 1)
                self.assertIn("sub2api-ray-" + role, mutations[0])
                self.assertEqual(mutations[0][-2:], [command, role])

    def test_stop_all_keeps_shared_services_and_volumes(self):
        self.run_script("stop", "all")
        self.assertEqual(len(self.mutations()), 2)
        for call in self.mutations():
            self.assertNotIn("sub2api-ray-infra", call)
            self.assertNotIn("down", call)
            self.assertNotIn("-v", call)

    def test_all_starts_infra_then_waits_for_master_before_salve(self):
        self.run_script("up", "all")
        calls = self.mutations()
        self.assertEqual([x[x.index("--project-name") + 1] for x in calls],
                         ["sub2api-ray-infra", "sub2api-ray-master", "sub2api-ray-salve"])
        self.assertTrue(all("--wait" in x for x in calls))

    def test_failed_master_bootstrap_does_not_start_salve(self):
        self.run_script("up", "all", success=False, MOCK_UP_FAIL_FOR="master")
        self.assertEqual(len(self.mutations()), 2)
        self.assertFalse(any("sub2api-ray-salve" in call for call in self.mutations()))

    def test_unavailable_infra_does_not_trigger_automatic_recreation(self):
        for env in ({"MOCK_MISSING_INFRA": "1"}, {"MOCK_INFRA_HEALTH": "false healthy"}, {"MOCK_INFRA_HEALTH": "true unhealthy"}):
            self.trace.write_text("", encoding="utf-8")
            self.run_script("up", "salve", success=False, **env)
            self.assertEqual(self.mutations(), [])

    def test_missing_image_fails_without_pulling_or_starting(self):
        self.run_script("up", "master", success=False, MOCK_IMAGE_MISSING="1")
        self.assertEqual(self.mutations(), [])

    def test_build_all_builds_once_and_tags_without_restarting(self):
        self.run_script("build", "all")
        calls = self.mutations()
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[0][-2:], ["build", "master"])
        self.assertEqual(calls[1], ["image", "tag", "sub2api-ray:master", "sub2api-ray:salve"])

    def test_config_validation_never_sources_env_as_shell(self):
        marker = self.root / "should-not-exist"
        self.env_file.write_text('PROBE=$(touch "' + marker.as_posix() + '")\n', encoding="utf-8")
        self.run_script("check")
        self.assertFalse(marker.exists())
        self.assertEqual(self.mutations(), [])
        self.assertEqual(sum("--quiet" in c for c in self.calls()), 3)

    def test_invalid_target_is_rejected_before_docker_calls(self):
        self.run_script("stop", "infra", success=False)
        self.assertEqual(self.calls(), [])

    def test_init_creates_fixed_secrets_and_never_overwrites_them(self):
        self.env_file.unlink()
        result = self.run_script("init")
        original = self.env_file.read_bytes()
        values = dict(line.split("=", 1) for line in original.decode().splitlines() if line and not line.startswith("#"))
        keys = ("POSTGRES_PASSWORD", "REDIS_PASSWORD", "JWT_SECRET", "TOTP_ENCRYPTION_KEY", "ADMIN_PASSWORD")
        secrets = [values[key] for key in keys]
        self.assertEqual(len(set(secrets)), 5)
        for secret in secrets:
            self.assertRegex(secret, r"^[0-9a-f]{48,64}$")
            self.assertNotIn(secret, result.stdout + result.stderr)
        self.assertNotIn(b"__GENERATE_", original)
        self.run_script("init")
        self.assertEqual(self.env_file.read_bytes(), original)
        if os.name != "nt":
            self.assertEqual(self.env_file.stat().st_mode & 0o777, 0o600)

    def test_backup_and_failure_cleanup_do_not_stop_services(self):
        self.run_script("backup")
        dumps = list((self.deploy / "backups").glob("*.dump"))
        self.assertEqual(len(dumps), 1)
        self.assertTrue(dumps[0].read_bytes().startswith(b"PGDMP"))
        self.run_script("backup", success=False, MOCK_BACKUP_FAIL="1")
        self.assertEqual(len(list((self.deploy / "backups").glob("*.dump"))), 1)
        self.assertEqual(list((self.deploy / "backups").glob(".partial.*")), [])
        self.assertTrue(all("exec" in c for c in self.mutations()))


if __name__ == "__main__":
    unittest.main()
