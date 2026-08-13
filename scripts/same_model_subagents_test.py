import importlib.util
import os
import subprocess
import sys
import unittest
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[1]
CHECKER_PATH = (
    REPOSITORY_ROOT
    / ".agents"
    / "skills"
    / "same-model-subagents"
    / "scripts"
    / "check_same_model.py"
)

SPEC = importlib.util.spec_from_file_location("check_same_model", CHECKER_PATH)
if SPEC is None or SPEC.loader is None:
    raise ImportError(f"cannot load checker from {CHECKER_PATH}")
CHECKER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECKER)
validate_dispatch = CHECKER.validate_dispatch


class SameModelPolicyTests(unittest.TestCase):
    def test_inherited_child_model_is_allowed(self):
        self.assertEqual(validate_dispatch("gpt-5.6-sol", None, True), None)

    def test_exact_explicit_child_model_is_allowed(self):
        self.assertEqual(
            validate_dispatch("gpt-5.6-sol", "gpt-5.6-sol", False), None
        )

    def test_different_explicit_child_model_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "must match parent model"):
            validate_dispatch("gpt-5.6-sol", "gpt-5.6-terra", False)

    def test_unknown_parent_model_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "parent model identity is required"):
            validate_dispatch("", None, True)

    def test_missing_child_selection_is_rejected(self):
        with self.assertRaisesRegex(
            ValueError, "inheritance or an explicit child model"
        ):
            validate_dispatch("gpt-5.6-sol", None, False)

    def test_cli_rejects_different_model_without_leaking_environment(self):
        secret = "do-not-print-this-credential"
        environment = os.environ.copy()
        environment["TOOLHUB_POLICY_PASSWORD"] = secret

        result = subprocess.run(
            [
                sys.executable,
                str(CHECKER_PATH),
                "--parent-model",
                "gpt-5.6-sol",
                "--child-model",
                "gpt-5.6-terra",
            ],
            cwd=REPOSITORY_ROOT,
            env=environment,
            check=False,
            capture_output=True,
            text=True,
        )

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must match parent model", result.stderr)
        self.assertNotIn(secret, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
