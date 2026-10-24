import copy
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


MODULE_PATH = Path(__file__).resolve().parents[1] / "prepare-existing.py"
spec = importlib.util.spec_from_file_location("prepare_existing", MODULE_PATH)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def fixture():
    containers = [
        {"Name": "/" + name, "State": {"Running": True, "Health": {"Status": "healthy"}},
         "NetworkSettings": {"Networks": {"production_sub2api-network": {}}}, "Config": {"Env": []}}
        for name in ["sub2api", "sub2api-postgres", "sub2api-redis"]
    ]
    containers[0]["Config"]["Env"] = [
        "DATABASE_USER=prod_user", "DATABASE_DBNAME=prod_db", "DATABASE_PASSWORD=secret$with${literal}#'quote",
        "JWT_SECRET=" + "a" * 64, "TOTP_ENCRYPTION_KEY=" + "b" * 64,
        "REDIS_PASSWORD=", "REDIS_DB=2", "RUN_MODE=simple", "ADMIN_PASSWORD=", "GATEWAY_FORCE_CODEX_CLI=true",
        "PATH=/old/image/path", "SKIP_SETUP=true",
    ]
    return containers


class ExistingDeploymentTests(unittest.TestCase):
    def test_prepare_writes_private_file_without_logging_secrets_or_overwriting(self):
        with tempfile.TemporaryDirectory(prefix="ray-existing-test-") as directory:
            root = Path(directory).resolve()
            self.assertEqual(root.parent, Path(tempfile.gettempdir()).resolve())
            deploy = root / "deploy-ray"
            deploy.mkdir()
            (root / "Dockerfile").write_text("FROM scratch\n", encoding="utf-8")
            output = io.StringIO()
            with patch.object(module, "DEPLOY_DIR", deploy), patch.object(module.sys, "argv", ["prepare-existing.py"]), \
                 patch.object(module.subprocess, "run", return_value=SimpleNamespace(stdout=json.dumps(fixture()))) as inspect, \
                 patch.object(module.subprocess, "check_output", return_value="abc123\n"), contextlib.redirect_stdout(output):
                module.main()
                target = deploy / "compose.existing.local.json"
                original = target.read_bytes()
                if os.name != "nt":
                    self.assertEqual(target.stat().st_mode & 0o777, 0o600)
                with self.assertRaisesRegex(ValueError, "already exists"):
                    module.main()
                self.assertEqual(target.read_bytes(), original)
                self.assertEqual(inspect.call_count, 1)
            self.assertNotIn("a" * 64, output.getvalue())
            self.assertNotIn("secret$", output.getvalue())

    def test_reuses_live_settings_and_existing_network_without_defining_infra(self):
        containers = fixture()
        original = copy.deepcopy(containers)
        cfg = module.make_config(containers, Path("/srv/sub2api"))
        self.assertEqual(containers, original)
        self.assertEqual(set(cfg["services"]), {"salve"})
        app = cfg["services"]["salve"]
        env = app["environment"]
        self.assertEqual(app["ports"], ["0.0.0.0:8081:8080"])
        self.assertNotIn("depends_on", app)
        self.assertEqual(cfg["networks"]["production"], {"name": "production_sub2api-network", "external": True})
        self.assertEqual(env["DATABASE_HOST"], "sub2api-postgres")
        self.assertEqual(env["REDIS_HOST"], "sub2api-redis")
        self.assertEqual(env["DATABASE_DBNAME"], "prod_db")
        self.assertEqual(env["DATABASE_PASSWORD"], "secret$$with$${literal}#'quote")
        self.assertEqual(env["REDIS_PASSWORD"], "")
        self.assertEqual(env["REDIS_DB"], "2")
        self.assertEqual(env["JWT_SECRET"], "a" * 64)
        self.assertEqual(env["TOTP_ENCRYPTION_KEY"], "b" * 64)
        self.assertEqual(env["RUN_MODE"], "simple")
        self.assertEqual(env["GATEWAY_FORCE_CODEX_CLI"], "true")
        self.assertNotIn("PATH", env)
        self.assertNotIn("SKIP_SETUP", env)
        self.assertEqual(env["DATABASE_MAX_OPEN_CONNS"], "20")

    def test_refuses_missing_shared_secrets(self):
        containers = fixture()
        containers[0]["Config"]["Env"] = [x for x in containers[0]["Config"]["Env"] if not x.startswith("JWT_SECRET=")]
        with self.assertRaisesRegex(ValueError, "JWT_SECRET"):
            module.make_config(containers, Path("/srv/sub2api"))

    def test_refuses_ambiguous_or_unshared_network(self):
        containers = fixture()
        for container in containers:
            container["NetworkSettings"]["Networks"]["another-network"] = {}
        with self.assertRaisesRegex(ValueError, "--network"):
            module.make_config(containers, Path("/srv/sub2api"))
        cfg = module.make_config(containers, Path("/srv/sub2api"), "another-network")
        self.assertEqual(cfg["networks"]["production"]["name"], "another-network")
        with self.assertRaisesRegex(ValueError, "already connect"):
            module.make_config(containers, Path("/srv/sub2api"), "missing")

    def test_refuses_unhealthy_infrastructure(self):
        containers = fixture()
        containers[1]["State"]["Health"]["Status"] = "unhealthy"
        with self.assertRaisesRegex(ValueError, "healthy"):
            module.make_config(containers, Path("/srv/sub2api"))


if __name__ == "__main__":
    unittest.main()
