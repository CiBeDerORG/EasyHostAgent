"""T01-02 native installer tests. All installation happens under a disposable staging root."""
import hashlib
import os
import pathlib
import shutil
import stat
import subprocess
import tempfile
import unittest

REPO = pathlib.Path(__file__).resolve().parents[2]
INSTALL = REPO / "scripts/install.sh"
UNINSTALL = REPO / "scripts/uninstall.sh"


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="easyhostagent-installer-")
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        self.stage = self.root / "stage"
        self.stage.mkdir()
        self.binary = self.root / "easyhostagent-test-binary"
        self.binary.write_text(
            "#!/bin/sh\n"
            "if [ \"$1\" = '-version' ]; then echo 'EasyHostAgent test'; exit 0; fi\n"
            "echo 'unexpected invocation' >&2\nexit 1\n",
            encoding="utf-8",
        )
        self.binary.chmod(0o755)

    def invoke(self, script, *args, env=None):
        merged = os.environ.copy()
        if env:
            merged.update(env)
        return subprocess.run(
            ["bash", str(script), *map(str, args)],
            cwd=REPO,
            capture_output=True,
            text=True,
            env=merged,
            timeout=20,
            check=False,
        )

    def install(self, *options, env=None):
        return self.invoke(
            INSTALL, "--binary", self.binary, "--staging-root", self.stage, *options,
            env=env,
        )

    def test_staged_install_has_binary_unit_config_and_private_data_dir(self):
        result = self.install(
            "--software-root", "/vol3/1000/software",
            "--data-dir", "/var/lib/easyhostagent",
            "--log-level", "debug",
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        binary = self.stage / "usr/local/bin/easyhostagent"
        unit = self.stage / "etc/systemd/system/easyhostagent.service"
        env = self.stage / "etc/easyhostagent/agent.env"
        state = self.stage / "var/lib/easyhostagent"
        marker = self.stage / "etc/easyhostagent/managed-by-installer"
        self.assertTrue(binary.is_file())
        self.assertTrue(unit.is_file())
        self.assertIn("ExecStart=/usr/local/bin/easyhostagent", unit.read_text())
        self.assertEqual(stat.S_IMODE(binary.stat().st_mode), 0o755)
        self.assertEqual(stat.S_IMODE(env.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(state.stat().st_mode), 0o700)
        self.assertIn("EASYHOSTAGENT_SOFTWARE_ROOT=/vol3/1000/software", env.read_text())
        self.assertEqual(marker.read_text(), "installed-by=EasyHostAgent\n")

    def test_update_retains_existing_identity_config_and_files(self):
        original = self.install()
        self.assertEqual(original.returncode, 0, original.stdout + original.stderr)
        state = self.stage / "var/lib/easyhostagent"
        (state / "agent-id").write_text("PERSIST-THIS-ID", encoding="utf-8")
        config = self.stage / "etc/easyhostagent/agent.env"
        before = config.read_bytes()
        self.binary.write_text("#!/bin/sh\n[ \"$1\" = -version ]\n")
        self.binary.chmod(0o755)
        again = self.install("--software-root", "/opt/changed", "--log-level", "error")
        self.assertEqual(again.returncode, 0, again.stdout + again.stderr)
        self.assertEqual(config.read_bytes(), before)
        self.assertEqual((state / "agent-id").read_text(), "PERSIST-THIS-ID")

    def test_uninstall_removes_only_installer_owned_binary_and_unit(self):
        installed = self.install()
        self.assertEqual(installed.returncode, 0, installed.stdout + installed.stderr)
        state = self.stage / "var/lib/easyhostagent"
        (state / "identity.json").write_text('{"id":"preserved"}')
        result = self.invoke(UNINSTALL, "--staging-root", self.stage)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse((self.stage / "usr/local/bin/easyhostagent").exists())
        self.assertFalse((self.stage / "etc/systemd/system/easyhostagent.service").exists())
        self.assertTrue((self.stage / "etc/easyhostagent/agent.env").exists())
        self.assertTrue((state / "identity.json").exists())
        self.assertFalse((self.stage / "etc/easyhostagent/managed-by-installer").exists())

    def test_install_rejects_missing_input_and_unsafe_root(self):
        blank = self.invoke(INSTALL, "--staging-root", self.stage)
        self.assertNotEqual(blank.returncode, 0)
        for bad in ("relative", "/", "/bad path", "/tmp/../danger"):
            with self.subTest(path=bad):
                out = self.invoke(INSTALL, "--binary", self.binary, "--staging-root", self.stage,
                                  "--software-root", bad)
                self.assertNotEqual(out.returncode, 0)
        self.assertFalse((self.stage / "usr/local/bin/easyhostagent").exists())

    def test_install_refuses_unowned_unit_or_binary(self):
        path = self.stage / "etc/systemd/system/easyhostagent.service"
        path.parent.mkdir(parents=True)
        path.write_text("Unmanaged unit", encoding="utf-8")
        out = self.install()
        self.assertNotEqual(out.returncode, 0)
        self.assertEqual(path.read_text(), "Unmanaged unit")

    def test_candidate_failure_does_not_replace_existing_binary(self):
        self.assertEqual(self.install().returncode, 0)
        binary_path = self.stage / "usr/local/bin/easyhostagent"
        original = binary_path.read_bytes()
        self.binary.write_text("#!/bin/sh\nexit 7\n")
        self.binary.chmod(0o755)
        fail = self.install()
        self.assertNotEqual(fail.returncode, 0)
        self.assertEqual(binary_path.read_bytes(), original)

    def release_env(self, corrupt=False):
        import platform
        arch = {"x86_64": "amd64", "aarch64": "arm64"}[platform.machine()]
        release_dir = self.root / "assets"
        release_dir.mkdir(exist_ok=True)
        filename = f"easyhostagent-linux-{arch}"
        target = release_dir / filename
        shutil.copyfile(self.binary, target)
        content = target.read_bytes()
        digest = hashlib.sha256(content).hexdigest()
        if corrupt:
            digest = "0" * 64 if digest != "0" * 64 else "f" * 64
        (release_dir / (filename + ".sha256")).write_text(
            f"{digest}  {filename}\n", encoding="utf-8"
        )
        mock = self.root / "bin"
        mock.mkdir(exist_ok=True)
        curl = mock / "curl"
        curl.write_text(
            "#!/usr/bin/env python3\n"
            "import os, shutil, sys\n"
            "args=sys.argv[1:]\n"
            "dest=args[args.index('-o')+1]\n"
            "source=os.path.join(os.environ['EHA_TEST_RELEASE_DIR'],args[-1].split('/')[-1])\n"
            "shutil.copyfile(source,dest)\n",
            encoding="utf-8",
        )
        curl.chmod(0o755)
        return {
            "EHA_TEST_RELEASE_DIR": str(release_dir),
            "PATH": str(mock) + os.pathsep + os.environ.get("PATH", ""),
        }

    def test_release_requires_matching_sha256(self):
        valid = self.invoke(INSTALL, "--release", "v1.2.3", "--staging-root", self.stage,
                            env=self.release_env())
        self.assertEqual(valid.returncode, 0, valid.stdout + valid.stderr)
        self.assertTrue((self.stage / "usr/local/bin/easyhostagent").exists())

    def test_release_rejects_bad_checksum_and_keeps_existing_binary(self):
        original = self.install()
        self.assertEqual(original.returncode, 0, original.stderr)
        binary_path = self.stage / "usr/local/bin/easyhostagent"
        before = binary_path.read_bytes()
        bad = self.invoke(INSTALL, "--release", "v1.2.3", "--staging-root", self.stage,
                          env=self.release_env(corrupt=True))
        self.assertNotEqual(bad.returncode, 0)
        self.assertIn("SHA-256 mismatch", bad.stderr)
        self.assertEqual(binary_path.read_bytes(), before)

    def test_uninstall_without_marker_never_deletes_foreign_files(self):
        path = self.stage / "usr/local/bin/easyhostagent"
        path.parent.mkdir(parents=True)
        path.write_text("not managed", encoding="utf-8")
        result = self.invoke(UNINSTALL, "--staging-root", self.stage)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(path.exists())

    def test_service_contains_required_systemd_fields(self):
        unit = (REPO / "packaging/systemd/easyhostagent.service").read_text()
        for required in ("[Unit]", "[Service]", "Type=simple", "Restart=on-failure",
                         "UMask=0077", "EnvironmentFile=-/etc/easyhostagent/agent.env",
                         "WantedBy=multi-user.target"):
            self.assertIn(required, unit)

    def test_compiled_binary_can_install_and_validate(self):
        real = os.environ.get("EHA_TEST_REAL_BINARY")
        if not real:
            self.skipTest("set EHA_TEST_REAL_BINARY to a locally built Go binary")
        result = self.invoke(INSTALL, "--binary", real, "--staging-root", self.stage)
        self.assertEqual(result.returncode, 0, result.stderr)
        binary = self.stage / "usr/local/bin/easyhostagent"
        run = subprocess.run([str(binary), "-check-config"], capture_output=True, text=True,
                             timeout=10, check=False)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertIn("configuration valid:", run.stdout)


if __name__ == "__main__":
    unittest.main()
