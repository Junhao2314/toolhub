import os
import json
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
BUILDER = ROOT / "scripts" / "build-frontend-kimi-bundle"


class FrontendKimiBundleTests(unittest.TestCase):
    def _write_skill(self, root: Path, slug: str, body: str = "---\nname: x\n---\n") -> Path:
        directory = root / slug
        directory.mkdir(parents=True)
        (directory / "SKILL.md").write_text(body, encoding="utf-8")
        return directory

    def _run_builder(self, hermes: Path, destination: Path, project_skill: Path | None = None) -> subprocess.CompletedProcess[str]:
        command = [str(BUILDER), "--hermes-skills", str(hermes), "--dest", str(destination)]
        if project_skill is not None:
            command.extend(["--project-skill-dir", str(project_skill)])
        return subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            env={**os.environ, "HOME": str(hermes.parent)},
        )

    def test_materializer_copies_only_allowlisted_skills(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            hermes = root / "hermes"
            destination = root / "bundle"
            for slug in ("ui-ux-pro-max-cn", "responsive-check", "performance-audit"):
                self._write_skill(hermes, slug)
            self._write_skill(hermes / "software-development", "browser-ui-verification")
            self._write_skill(hermes, "unrelated-skill")

            result = self._run_builder(hermes, destination)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(
                sorted(path.name for path in destination.iterdir()),
                ["browser-ui-verification", "performance-audit", "responsive-check", "ui-ux-pro-max-cn"],
            )
            self.assertFalse((destination / "unrelated-skill").exists())

    def test_materializer_adds_project_skill_only_when_explicit(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            hermes = root / "hermes"
            project_skill = root / "project" / ".agents" / "skills" / "toolhub-frontend"
            destination = root / "bundle"
            for slug in ("ui-ux-pro-max-cn", "responsive-check", "performance-audit"):
                self._write_skill(hermes, slug)
            self._write_skill(hermes / "software-development", "browser-ui-verification")
            self._write_skill(project_skill.parent, project_skill.name)

            result = self._run_builder(hermes, destination, project_skill)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(
                sorted(path.name for path in destination.iterdir()),
                ["browser-ui-verification", "performance-audit", "responsive-check", "toolhub-frontend", "ui-ux-pro-max-cn"],
            )

    def test_materializer_fails_closed_and_preserves_active_bundle(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            hermes = root / "hermes"
            destination = root / "bundle"
            for slug in ("ui-ux-pro-max-cn", "responsive-check", "performance-audit"):
                self._write_skill(hermes, slug)
            self._write_skill(hermes / "software-development", "browser-ui-verification")
            first = self._run_builder(hermes, destination)
            self.assertEqual(first.returncode, 0, first.stderr)

            (hermes / "ui-ux-pro-max-cn" / "SKILL.md").unlink()
            second = self._run_builder(hermes, destination)
            self.assertNotEqual(second.returncode, 0)
            self.assertTrue((destination / "ui-ux-pro-max-cn" / "SKILL.md").exists())

    def test_materializer_refuses_to_replace_a_skill_root(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            hermes = root / "hermes"
            for slug in ("ui-ux-pro-max-cn", "responsive-check", "performance-audit"):
                self._write_skill(hermes, slug)
            self._write_skill(hermes / "software-development", "browser-ui-verification")

            result = self._run_builder(hermes, hermes)
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue((hermes / "ui-ux-pro-max-cn" / "SKILL.md").exists())

    def test_materializer_uses_ui_config_for_kimi_bundle(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            hermes = root / "hermes"
            destination = root / "bundle"
            for slug in ("ui-ux-pro-max-cn", "responsive-check", "custom-ui"):
                self._write_skill(hermes, slug)
            config = root / "settings.json"
            config.write_text(json.dumps({"kimiFrontendBundle": ["ui-ux-pro-max-cn", "custom-ui"]}), encoding="utf-8")
            result = subprocess.run(
                [str(BUILDER), "--hermes-skills", str(hermes), "--config", str(config), "--dest", str(destination)],
                check=False,
                capture_output=True,
                text=True,
                env={**os.environ, "HOME": str(root)},
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(sorted(path.name for path in destination.iterdir()), ["custom-ui", "ui-ux-pro-max-cn"])

    def test_materializer_supports_pi_bundle_from_ui_config(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pi = root / "pi-skills"
            destination = root / "pi-bundle"
            self._write_skill(pi, "officecli")
            self._write_skill(pi, "unused")
            config = root / "settings.json"
            config.write_text(json.dumps({"piBundle": ["officecli"]}), encoding="utf-8")
            result = subprocess.run(
                [str(BUILDER), "--bundle", "pi", "--pi-skills", str(pi), "--config", str(config), "--dest", str(destination)],
                check=False,
                capture_output=True,
                text=True,
                env={**os.environ, "HOME": str(root)},
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual([path.name for path in destination.iterdir()], ["officecli"])


if __name__ == "__main__":
    unittest.main()
