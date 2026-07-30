import importlib.util
import json
import os
import pathlib
import pwd
import tempfile
import types
import unittest
from unittest import mock


MODULE_PATH = pathlib.Path(__file__).parents[3] / "internal" / "saltdriver" / "assets" / "_modules" / "toolhub.py"


def load_module():
    spec = importlib.util.spec_from_file_location("toolhub_salt_module", MODULE_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ToolHubSaltModuleTests(unittest.TestCase):
    def setUp(self):
        self.module = load_module()
        self.root = tempfile.TemporaryDirectory()
        self.addCleanup(self.root.cleanup)
        base = pathlib.Path(self.root.name)
        self.home = base / "home"
        self.staging = base / "staging"
        self.backups = base / "backups"
        self.home.mkdir()
        self.staging.mkdir()
        self.backups.mkdir()
        self.module.STAGING_ROOT = str(self.staging)
        self.module.BACKUP_ROOT = str(self.backups)
        current = pwd.getpwuid(os.getuid())
        self.user = types.SimpleNamespace(pw_name=current.pw_name, pw_uid=current.pw_uid,
                                          pw_gid=current.pw_gid, pw_dir=str(self.home))
        self.user_patch = mock.patch.object(self.module.pwd, "getpwnam", return_value=self.user)
        self.user_patch.start()
        self.addCleanup(self.user_patch.stop)

    def bundle(self, value, name="bundle.json"):
        path = self.staging / name
        path.write_text(json.dumps(value), encoding="utf-8")
        path.chmod(0o600)
        return str(path)

    def target(self, runtime="claude"):
        return {"id": "target-id", "nodeId": "node-id", "nodeKind": "salt",
                "saltMinionId": "minion", "runtime": runtime,
                "managedUsername": self.user.pw_name}

    def test_scan_rejects_staging_escape(self):
        outside = pathlib.Path(self.root.name) / "outside.json"
        outside.write_text("{}", encoding="utf-8")
        result = self.module.scan(str(outside))
        self.assertFalse(result["ok"])
        self.assertEqual(result["error"]["code"], "invalid_request")

    def test_scan_rejects_managed_home_mismatch_and_symlink_parent(self):
        target = self.target("codex")
        target["managedHome"] = str(self.home / "other")
        result = self.module.scan(self.bundle({"target": target}, "home-mismatch.json"))
        self.assertFalse(result["ok"])

        target["managedHome"] = str(self.home)
        outside = pathlib.Path(self.root.name) / "outside-codex"
        outside.mkdir()
        (self.home / ".codex").symlink_to(outside, target_is_directory=True)
        result = self.module.scan(self.bundle({"target": target}, "symlink-parent.json"))
        self.assertFalse(result["ok"])
        self.assertIn("symlink", result["error"]["message"])

    def test_hermes_preflight_is_read_only(self):
        path = self.bundle({"target": self.target("hermes"), "manifest": {"target": self.target("hermes"), "skills": []}})
        result = self.module.preflight(path)
        self.assertFalse(result["ok"])
        self.assertEqual(result["error"]["code"], "hermes_read_only")

    def test_claude_writer_preserves_non_mcp_fields_and_unmanaged_on_reconcile(self):
        config = self.home / ".claude.json"
        config.write_text(json.dumps({"theme": "dark", "mcpServers": {"local": {"command": "local"}, "managed": {"command": "old"}}}), encoding="utf-8")
        desired = {"managed": {"command": "new", "args": []}}
        self.module._write_claude_mcp(str(config), desired, True, os.getuid(), os.getgid())
        root = json.loads(config.read_text(encoding="utf-8"))
        self.assertEqual(root["theme"], "dark")
        self.assertEqual(root["mcpServers"]["local"]["command"], "local")
        self.assertEqual(root["mcpServers"]["managed"]["command"], "new")

    def test_codex_writer_preserves_other_sections_and_mirrors_mcp(self):
        config = self.home / ".codex" / "config.toml"
        config.parent.mkdir()
        config.write_text('model = "keep"\n\n[mcp_servers.old]\ncommand = "old"\n\n[projects.keep]\ntrusted = true\n', encoding="utf-8")
        desired = {"new": {"url": "https://example.invalid/mcp"}}
        self.module._write_codex_mcp(str(config), desired, False, os.getuid(), os.getgid())
        text = config.read_text(encoding="utf-8")
        self.assertIn('model = "keep"', text)
        self.assertIn("[projects.keep]", text)
        self.assertNotIn("[mcp_servers.old]", text)
        self.assertIn("[mcp_servers.new]", text)

    def test_secret_resolver_requires_every_reference(self):
        manifest = {"target": self.target("claude"), "mcpServers": [{"name": "server", "transport": "stdio", "command": "tool", "envRefs": {"TOKEN": "secret-id"}}]}
        with self.assertRaisesRegex(ValueError, "missing MCP environment secret"):
            self.module._resolved_mcp(manifest, {})
        resolved = self.module._resolved_mcp(manifest, {"secret-id": "plaintext-once"})
        self.assertEqual(resolved["server"]["env"]["TOKEN"], "plaintext-once")

    def test_content_hash_is_mode_and_content_sensitive(self):
        skill = self.home / "skill"
        skill.mkdir()
        file = skill / "SKILL.md"
        file.write_text("hello", encoding="utf-8")
        file.chmod(0o644)
        first = self.module._content_hash(str(skill))
        file.chmod(0o755)
        second = self.module._content_hash(str(skill))
        self.assertNotEqual(first, second)

    def test_restore_rolls_back_skills_when_mcp_write_fails(self):
        target = self.target("claude")
        skills = self.home / ".claude" / "skills"
        current_skill = skills / "current"
        current_skill.mkdir(parents=True)
        (current_skill / "SKILL.md").write_text("current", encoding="utf-8")
        config = self.home / ".claude.json"
        current_config = json.dumps({"mcpServers": {"current": {"command": "current"}}})
        config.write_text(current_config, encoding="utf-8")

        selected = self.backups / target["id"] / "selected"
        restored_skill = selected / "skills" / "restored"
        restored_skill.mkdir(parents=True)
        (restored_skill / "SKILL.md").write_text("restored", encoding="utf-8")
        (selected / "mcp-config").write_text(
            json.dumps({"mcpServers": {"restored": {"command": "restored"}}}),
            encoding="utf-8",
        )

        original_atomic_write = self.module._atomic_write
        calls = 0

        def fail_selected_mcp_once(*args, **kwargs):
            nonlocal calls
            calls += 1
            if calls == 1:
                raise OSError("injected MCP write failure")
            return original_atomic_write(*args, **kwargs)

        current = self.module._scan_target(str(skills), str(self.home), "claude")
        path = self.bundle({"operationId": "restore-op", "target": target,
                            "backupId": "selected", "expectedRevision": current["targetRevision"],
                            "manifest": {"target": target, "skills": [], "mcpServers": []}})
        with mock.patch.object(self.module, "_atomic_write", side_effect=fail_selected_mcp_once):
            result = self.module.restore(path)

        self.assertFalse(result["ok"])
        self.assertEqual(result["error"]["code"], "backup_failed")
        self.assertTrue((skills / "current" / "SKILL.md").exists())
        self.assertFalse((skills / "restored").exists())
        self.assertEqual(json.loads(config.read_text(encoding="utf-8")),
                         json.loads(current_config))

    def test_restore_revision_conflict_creates_no_recovery_backup(self):
        target = self.target("claude")
        skills = self.home / ".claude" / "skills"
        skills.mkdir(parents=True)
        selected = self.backups / target["id"] / "selected"
        (selected / "skills").mkdir(parents=True)
        before = sorted(path.name for path in selected.parent.iterdir())
        path = self.bundle({"operationId": "restore-op", "target": target,
                            "backupId": "selected", "expectedRevision": "0" * 64,
                            "manifest": {"target": target, "skills": [], "mcpServers": []}},
                           "restore-conflict.json")
        result = self.module.restore(path)
        self.assertFalse(result["ok"])
        self.assertEqual(result["error"]["code"], "revision_conflict")
        self.assertEqual(before, sorted(path.name for path in selected.parent.iterdir()))

    def test_restore_verifies_pinned_managed_content(self):
        target = self.target("claude")
        skills = self.home / ".claude" / "skills"
        current = skills / "current"
        current.mkdir(parents=True)
        (current / "SKILL.md").write_text("current", encoding="utf-8")
        selected = self.backups / target["id"] / "selected"
        restored = selected / "skills" / "restored"
        restored.mkdir(parents=True)
        (restored / "SKILL.md").write_text("restored", encoding="utf-8")
        content_hash = self.module._content_hash(str(restored))
        revision = self.module._scan_target(str(skills), str(self.home), "claude")["targetRevision"]
        manifest = {"target": target, "skills": [{"slug": "restored", "contentHash": content_hash}], "mcpServers": []}
        path = self.bundle({"operationId": "restore-op", "target": target,
                            "backupId": "selected", "expectedRevision": revision,
                            "manifest": manifest}, "restore-verified.json")
        result = self.module.restore(path)
        self.assertTrue(result["ok"], result)
        self.assertTrue((skills / "restored" / "SKILL.md").exists())
        self.assertFalse((skills / "current").exists())


if __name__ == "__main__":
    unittest.main()
