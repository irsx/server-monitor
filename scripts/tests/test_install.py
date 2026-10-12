"""Exercise the installer with fake downloads/system commands in a temporary root."""

import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


PROJECT = Path(__file__).resolve().parents[2]
MOCK = r'''
import hashlib, os, pathlib, shutil, sys
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ["MOCK_LOG"], "a") as log:
    log.write(name + " " + " ".join(args) + "\n")
if name == "uname":
    print("Linux" if args == ["-s"] else os.environ.get("MOCK_ARCH", "x86_64"))
elif name == "id":
    if args == ["-u"]: print("0")
    elif os.environ.get("MOCK_USER_MISSING") == "1": sys.exit(1)
elif name == "getent" and os.environ.get("MOCK_USER_MISSING") == "1":
    sys.exit(2)
elif name == "curl":
    if args[-1].endswith("/latest"):
        print("https://github.com/irsx/server-monitor/releases/tag/v1.2.3", end="")
    else:
        source = pathlib.Path(os.environ["MOCK_ASSETS"]) / args[-1].split("/")[-1]
        if not source.exists(): sys.exit(22)
        shutil.copyfile(source, args[args.index("--output") + 1])
elif name == "sha256sum":
    checksum, filename = pathlib.Path(args[-1]).read_text().split()
    sys.exit(0 if hashlib.sha256(pathlib.Path(filename).read_bytes()).hexdigest() == checksum else 1)
elif name == "install":
    paths = []
    mode = 0o755
    i = 0
    while i < len(args):
        if args[i] in ("-o", "-g", "-m"):
            if args[i] == "-m": mode = int(args[i+1], 8)
            i += 2
        elif args[i] == "-d": i += 1
        else:
            paths.append(pathlib.Path(args[i]))
            i += 1
    if "-d" in args:
        for path in paths: path.mkdir(parents=True, exist_ok=True)
    else:
        shutil.copyfile(paths[0], paths[1])
        paths[1].chmod(mode)
elif name == "systemctl" and os.environ.get("MOCK_SERVICE_FAIL") == "1" and args[0] == "restart":
    sys.exit(1)
'''


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        commands = self.root / "commands"
        commands.mkdir()
        self.assets = self.root / "assets"
        self.assets.mkdir()
        for arch in ("amd64", "arm64"):
            (self.assets / f"service-health-monitor-linux-{arch}").write_text(f"binary-{arch}")
        (self.assets / "server-monitor.env.example").write_bytes((PROJECT / ".env.example").read_bytes())
        (self.assets / "server-monitor.service").write_bytes((PROJECT / "deploy/server-monitor.service").read_bytes())
        manifest = "".join(f"{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n" for p in sorted(self.assets.iterdir()))
        (self.assets / "checksums.txt").write_text(manifest)
        for command in ("uname", "id", "curl", "sha256sum", "install", "systemctl", "getent", "useradd", "groupadd"):
            path = commands / command
            path.write_text(f"#!{sys.executable}\n" + MOCK)
            path.chmod(0o755)
        # Redirect fixed system paths only in the test copy; production has no staging bypass.
        script = (PROJECT / "scripts/install.sh").read_text()
        for directory in ("/etc", "/opt", "/run"):
            script = script.replace(directory + "/", str(self.root) + directory + "/")
        self.script = self.root / "install.sh"
        self.script.write_text(script)
        (self.root / "run/systemd/system").mkdir(parents=True)
        (self.root / "etc/systemd/system").mkdir(parents=True)
        self.env = dict(os.environ, PATH=f"{commands}:{os.environ['PATH']}", MOCK_ASSETS=str(self.assets), MOCK_LOG=str(self.root / "commands.log"))
        self.config = self.root / "etc/server-monitor/server-monitor.env"
        self.binary = self.root / "opt/server-monitor/bin/service-health-monitor"

    def run_installer(self, *args):
        return subprocess.run(["bash", str(self.script), *args], env=self.env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=30)

    def commands(self):
        return (self.root / "commands.log").read_text()

    def env_file(self):
        source = self.root / "input.env"
        source.write_text("TELEGRAM_BOT_TOKEN=123:test\nTELEGRAM_CHAT_ID=-123\n")
        return str(source)

    def test_fresh_install_with_config_downloads_latest_and_starts(self):
        self.env["MOCK_USER_MISSING"] = "1"
        result = self.run_installer("--env-file", self.env_file())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.binary.read_text(), "binary-amd64")
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o640)
        self.assertTrue((self.root / "etc/systemd/system/server-monitor.service").exists())
        self.assertIn("/releases/download/v1.2.3/", self.commands())
        self.assertIn("systemctl enable server-monitor.service", self.commands())
        self.assertIn("systemctl restart server-monitor.service", self.commands())
        self.assertIn("groupadd --system server-monitor", self.commands())
        self.assertIn("useradd --system --gid server-monitor", self.commands())

    def test_noninteractive_install_without_config_does_not_start(self):
        result = self.run_installer("--version", "v1.0.0")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("MYSQL_ENABLED=false", self.config.read_text())
        self.assertIn("systemctl daemon-reload", self.commands())
        self.assertNotIn("systemctl enable", self.commands())
        self.assertNotIn("systemctl restart", self.commands())

    def test_update_preserves_config_and_supports_arm64_no_start(self):
        self.config.parent.mkdir(parents=True)
        self.config.write_text("existing secrets and custom settings\n")
        self.env["MOCK_ARCH"] = "aarch64"
        result = self.run_installer("--version", "v2.0.0", "--env-file", self.env_file(), "--no-start")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.config.read_text(), "existing secrets and custom settings\n")
        self.assertEqual(self.binary.read_text(), "binary-arm64")
        self.assertNotIn("systemctl restart", self.commands())

    def test_corrupt_download_leaves_installation_untouched(self):
        self.binary.parent.mkdir(parents=True)
        self.binary.write_text("old working binary")
        (self.assets / "service-health-monitor-linux-amd64").write_text("corrupt")
        result = self.run_installer("--version", "v1.0.0")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum gagal", result.stderr)
        self.assertEqual(self.binary.read_text(), "old working binary")
        self.assertNotIn("systemctl ", self.commands())

    def test_missing_asset_fails_before_installation(self):
        (self.assets / "server-monitor.service").unlink()
        result = self.run_installer("--version", "v1.0.0")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.binary.exists())

    def test_missing_checksum_fails_before_installation(self):
        (self.assets / "checksums.txt").write_text("")
        result = self.run_installer("--version", "v1.0.0")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.binary.exists())

    def test_unsupported_architecture_fails_without_download(self):
        self.env["MOCK_ARCH"] = "riscv64"
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("curl ", self.commands())

    def test_service_failure_returns_nonzero(self):
        self.env["MOCK_SERVICE_FAIL"] = "1"
        result = self.run_installer("--env-file", self.env_file())
        self.assertNotEqual(result.returncode, 0)

    def test_rejects_invalid_arguments(self):
        for args in (("--version",), ("--version", "v1/../../x"), ("--repo", "invalid"), ("--unknown",)):
            with self.subTest(args=args):
                self.assertNotEqual(self.run_installer(*args).returncode, 0)


if __name__ == "__main__":
    unittest.main()
