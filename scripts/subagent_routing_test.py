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
    / "subagent-routing"
    / "scripts"
    / "check_subagent_routing.py"
)

SPEC = importlib.util.spec_from_file_location("check_subagent_routing", CHECKER_PATH)
if SPEC is None or SPEC.loader is None:
    raise ImportError(f"cannot load checker from {CHECKER_PATH}")
CHECKER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECKER)
validate_dispatch = CHECKER.validate_dispatch


class SubagentRoutingTests(unittest.TestCase):
    def test_planner_and_reviewer_require_exact_parent_model(self):
        self.assertIsNone(
            validate_dispatch("planner", parent_model="gpt-5.6-sol", child_model="gpt-5.6-sol")
        )
        self.assertIsNone(
            validate_dispatch("reviewer", parent_model="gpt-5.6-sol", inherits_parent=True)
        )

    def test_parent_route_rejects_other_model_and_cli(self):
        with self.assertRaisesRegex(ValueError, "match parent model"):
            validate_dispatch("reviewer", parent_model="gpt-5.6-sol", child_model="gpt-5.6-flash")
        with self.assertRaisesRegex(ValueError, "not a CLI"):
            validate_dispatch("planner", parent_model="gpt-5.6-sol", command="hermes", args=["-z"])
        with self.assertRaisesRegex(ValueError, "inheritance or an explicit"):
            validate_dispatch("planner", parent_model="gpt-5.6-sol", child_model="gpt-5.6-sol", inherits_parent=True)

    def test_executor_uses_hermes_default_without_model_override(self):
        self.assertIsNone(validate_dispatch("executor", command="hermes", args=["--skills", "subagent-routing", "-z", "$PROMPT"]))
        self.assertIsNone(validate_dispatch("coding", command="/root/.local/bin/hermes", args=["--skills=subagent-routing", "--oneshot", "prompt"]))

    def test_frontend_uses_kimi_default_without_model_override(self):
        self.assertIsNone(validate_dispatch("frontend", command="kimi", args=["--skills-dir", "/tmp/frontend-kimi", "-p", "prompt"]))

    def test_research_uses_agy_gemini_default_without_model_override(self):
        self.assertIsNone(validate_dispatch("research", command="agy", args=["--print", "prompt"]))
        self.assertIsNone(validate_dispatch("资料查阅", command="agy", args=["--prompt", "prompt"]))

    def test_cli_routes_reject_parent_model_and_model_flags(self):
        cases = [
            ("executor", "hermes", ["--skills", "subagent-routing", "-z", "--model", "gpt-5.6-sol"]),
            ("frontend", "kimi", ["--skills-dir", "/tmp/frontend-kimi", "-p", "--model=gpt-5.6-sol"]),
            ("research", "agy", ["--print", "-mgpt-5.6-sol"]),
        ]
        for role, command, argv in cases:
            with self.subTest(role=role):
                with self.assertRaisesRegex(ValueError, "must not pass"):
                    validate_dispatch(role, command=command, args=argv)
        with self.assertRaisesRegex(ValueError, "configured default"):
            validate_dispatch("executor", command="hermes", args=["--skills", "subagent-routing", "-z"], child_model="gpt-5.6-sol")

    def test_cli_routes_require_the_prompt_mode(self):
        with self.assertRaisesRegex(ValueError, "requires"):
            validate_dispatch("frontend", command="kimi", args=["--skills-dir", "/tmp/frontend-kimi", "prompt"])
        with self.assertRaisesRegex(ValueError, "must use the agy CLI"):
            validate_dispatch("research", command="gemini", args=["--print", "prompt"])

    def test_unknown_or_unavailable_routes_fail_closed(self):
        with self.assertRaisesRegex(ValueError, "unsupported"):
            validate_dispatch("random", parent_model="gpt-5.6-sol", inherits_parent=True)
        with self.assertRaisesRegex(ValueError, "not executable"):
            validate_dispatch("research", command="/definitely/missing/agy", args=["--print", "prompt"], check_executable=True)

    def test_checker_does_not_leak_environment_secrets(self):
        secret = "do-not-print-this-credential"
        environment = os.environ.copy()
        environment["TOOLHUB_POLICY_PASSWORD"] = secret
        result = subprocess.run(
            [
                sys.executable,
                str(CHECKER_PATH),
                "--role",
                "executor",
                "--command",
                "hermes",
                "--arg=--skills",
                "--arg=subagent-routing",
                "--arg=-z",
                "--arg=--model",
                "--arg",
                secret,
            ],
            cwd=REPOSITORY_ROOT,
            env=environment,
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must not pass", result.stderr)
        self.assertNotIn(secret, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
