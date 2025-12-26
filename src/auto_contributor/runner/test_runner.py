"""Multi-framework test runner."""

import asyncio
import json
import time
from dataclasses import dataclass, field
from enum import Enum
from pathlib import Path

import structlog

from auto_contributor.core.exceptions import TestError

logger = structlog.get_logger(__name__)


class TestFramework(str, Enum):
    """Supported test frameworks."""

    PYTEST = "pytest"
    NPM = "npm"
    PNPM = "pnpm"
    YARN = "yarn"
    CARGO = "cargo"
    GO = "go"
    MAVEN = "maven"
    GRADLE = "gradle"
    UNKNOWN = "unknown"


@dataclass
class TestResult:
    """Result of running tests."""

    passed: bool
    framework: TestFramework
    output: str
    duration: float
    failed_tests: list[str] = field(default_factory=list)
    exit_code: int = 0


class TestRunner:
    """Detects and runs tests for various project types."""

    # Files that indicate a specific test framework
    DETECTION_FILES: dict[TestFramework, list[str]] = {
        TestFramework.PYTEST: ["pytest.ini", "pyproject.toml", "setup.py", "setup.cfg"],
        TestFramework.NPM: ["package.json"],
        TestFramework.PNPM: ["pnpm-lock.yaml"],
        TestFramework.YARN: ["yarn.lock"],
        TestFramework.CARGO: ["Cargo.toml"],
        TestFramework.GO: ["go.mod"],
        TestFramework.MAVEN: ["pom.xml"],
        TestFramework.GRADLE: ["build.gradle", "build.gradle.kts"],
    }

    # Commands to run tests for each framework
    TEST_COMMANDS: dict[TestFramework, list[str]] = {
        TestFramework.PYTEST: ["python", "-m", "pytest", "-x", "--tb=short", "-q"],
        TestFramework.NPM: ["npm", "test", "--", "--passWithNoTests"],
        TestFramework.PNPM: ["pnpm", "test", "--passWithNoTests"],
        TestFramework.YARN: ["yarn", "test", "--passWithNoTests"],
        TestFramework.CARGO: ["cargo", "test", "--", "--test-threads=1"],
        TestFramework.GO: ["go", "test", "-v", "./..."],
        TestFramework.MAVEN: ["mvn", "test", "-q"],
        TestFramework.GRADLE: ["./gradlew", "test"],
    }

    def __init__(self, timeout: int = 300, install_timeout: int = 300):
        self.timeout = timeout
        self.install_timeout = install_timeout
        self._installed_repos: set[str] = set()  # Track repos where we've installed deps

    def detect_framework(self, repo_path: Path) -> TestFramework:
        """Detect which test framework a repository uses."""
        # Check for pnpm/yarn first (more specific than npm)
        for framework in [TestFramework.PNPM, TestFramework.YARN]:
            for filename in self.DETECTION_FILES[framework]:
                if (repo_path / filename).exists():
                    return framework

        # Check other frameworks
        for framework, files in self.DETECTION_FILES.items():
            if framework in [TestFramework.PNPM, TestFramework.YARN]:
                continue

            for filename in files:
                if (repo_path / filename).exists():
                    # Special check for Python - verify pytest is available
                    if framework == TestFramework.PYTEST:
                        if self._has_pytest_config(repo_path):
                            return framework
                    else:
                        return framework

        return TestFramework.UNKNOWN

    def _has_pytest_config(self, repo_path: Path) -> bool:
        """Check if the project has pytest configured."""
        # Check pyproject.toml for pytest config
        pyproject = repo_path / "pyproject.toml"
        if pyproject.exists():
            content = pyproject.read_text()
            if "[tool.pytest" in content or "pytest" in content:
                return True

        # Check for pytest.ini
        if (repo_path / "pytest.ini").exists():
            return True

        # Check for tests directory
        if (repo_path / "tests").is_dir() or (repo_path / "test").is_dir():
            return True

        return False

    def _has_npm_test_script(self, repo_path: Path) -> bool:
        """Check if npm/pnpm/yarn project has a test script defined."""
        package_json = repo_path / "package.json"
        if not package_json.exists():
            return False

        try:
            data = json.loads(package_json.read_text())
            scripts = data.get("scripts", {})
            # Check if "test" script exists and is not a placeholder
            test_script = scripts.get("test", "")
            if not test_script:
                return False
            # Common placeholder patterns that indicate no real tests
            placeholders = [
                "echo \"Error: no test specified\"",
                "echo 'Error: no test specified'",
                "exit 1",
                "no test",
            ]
            for placeholder in placeholders:
                if placeholder in test_script.lower():
                    return False
            return True
        except (json.JSONDecodeError, OSError) as e:
            logger.warning("failed_to_parse_package_json", error=str(e))
            return False

    async def _install_dependencies(
        self, repo_path: Path, framework: TestFramework
    ) -> bool:
        """Install dependencies for npm-based projects. Returns True if successful."""
        repo_key = str(repo_path)
        if repo_key in self._installed_repos:
            return True

        # Determine install command based on framework
        install_commands = {
            TestFramework.NPM: ["npm", "install"],
            TestFramework.PNPM: ["pnpm", "install"],
            TestFramework.YARN: ["yarn", "install"],
        }

        if framework not in install_commands:
            return True  # No install needed for non-npm frameworks

        command = install_commands[framework]
        logger.info(
            "installing_dependencies",
            framework=framework.value,
            command=" ".join(command),
            path=str(repo_path),
        )

        try:
            process = await asyncio.create_subprocess_exec(
                *command,
                cwd=str(repo_path),
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.STDOUT,
                env=self._get_test_env(),
            )

            stdout, _ = await asyncio.wait_for(
                process.communicate(),
                timeout=self.install_timeout,
            )

            if process.returncode == 0:
                self._installed_repos.add(repo_key)
                logger.info("dependencies_installed", framework=framework.value)
                return True
            else:
                output = stdout.decode(errors="replace")
                logger.warning(
                    "dependency_install_failed",
                    framework=framework.value,
                    exit_code=process.returncode,
                    output=output[:500],
                )
                return False

        except asyncio.TimeoutError:
            logger.error(
                "dependency_install_timeout",
                framework=framework.value,
                timeout=self.install_timeout,
            )
            return False
        except Exception as e:
            logger.error("dependency_install_error", error=str(e))
            return False

    async def run_tests(
        self, repo_path: Path, files_changed: list[str] | None = None
    ) -> TestResult:
        """
        Run tests for a repository.

        Args:
            repo_path: Path to the repository
            files_changed: List of changed files to determine which packages to test

        Returns:
            TestResult with pass/fail status and output
        """
        framework = self.detect_framework(repo_path)

        if framework == TestFramework.UNKNOWN:
            logger.warning("no_test_framework_detected", path=str(repo_path))
            return TestResult(
                passed=True,  # No tests = pass (can't verify)
                framework=framework,
                output="No test framework detected",
                duration=0.0,
            )

        # Check if npm-based projects have a test script
        if framework in [TestFramework.NPM, TestFramework.PNPM, TestFramework.YARN]:
            if not self._has_npm_test_script(repo_path):
                logger.info(
                    "no_test_script_in_package_json",
                    framework=framework.value,
                    path=str(repo_path),
                )
                return TestResult(
                    passed=True,  # No test script = skip tests (common for docs/config repos)
                    framework=framework,
                    output="No test script defined in package.json, skipping tests",
                    duration=0.0,
                )

            # Install dependencies before running tests
            install_success = await self._install_dependencies(repo_path, framework)
            if not install_success:
                return TestResult(
                    passed=False,
                    framework=framework,
                    output="Failed to install dependencies",
                    duration=0.0,
                    exit_code=-1,
                )

        # Get the appropriate test command, customized for affected files
        command = self._get_test_command(framework, repo_path, files_changed)
        logger.info("running_tests", framework=framework.value, command=" ".join(command))

        start_time = time.time()

        try:
            process = await asyncio.create_subprocess_exec(
                *command,
                cwd=str(repo_path),
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.STDOUT,
                env=self._get_test_env(),
            )

            stdout, _ = await asyncio.wait_for(
                process.communicate(),
                timeout=self.timeout,
            )

            duration = time.time() - start_time
            output = stdout.decode(errors="replace")
            passed = process.returncode == 0

            failed_tests = self._extract_failures(output, framework) if not passed else []

            return TestResult(
                passed=passed,
                framework=framework,
                output=output,
                duration=duration,
                failed_tests=failed_tests,
                exit_code=process.returncode or 0,
            )

        except asyncio.TimeoutError:
            duration = time.time() - start_time
            return TestResult(
                passed=False,
                framework=framework,
                output=f"Tests timed out after {self.timeout} seconds",
                duration=duration,
                exit_code=-1,
            )

        except Exception as e:
            duration = time.time() - start_time
            logger.error("test_execution_failed", error=str(e))
            return TestResult(
                passed=False,
                framework=framework,
                output=str(e),
                duration=duration,
                exit_code=-1,
            )

    def _get_test_command(
        self,
        framework: TestFramework,
        repo_path: Path,
        files_changed: list[str] | None = None,
    ) -> list[str]:
        """Get the test command, customized for affected packages."""
        base_command = self.TEST_COMMANDS[framework].copy()

        # For Go projects, run only affected package tests instead of ./...
        if framework == TestFramework.GO and files_changed:
            packages = self._extract_go_packages(files_changed)
            if packages:
                # Replace ./... with specific packages
                # Format: go test -v ./path/to/package/...
                package_args = [f"./{pkg}/..." for pkg in packages]
                base_command = ["go", "test", "-v", "-timeout", "180s"] + package_args
                logger.info(
                    "running_targeted_go_tests",
                    packages=packages,
                    command=" ".join(base_command),
                )
            else:
                # Fallback to running tests in the root package only
                base_command = ["go", "test", "-v", "-timeout", "180s", "."]

        # For pytest, run only affected test files or directories
        elif framework == TestFramework.PYTEST and files_changed:
            test_paths = self._extract_python_test_paths(files_changed, repo_path)
            if test_paths:
                base_command = ["python", "-m", "pytest", "-x", "--tb=short", "-q"] + test_paths
                logger.info(
                    "running_targeted_pytest",
                    paths=test_paths,
                    command=" ".join(base_command),
                )

        return base_command

    def _extract_go_packages(self, files_changed: list[str]) -> list[str]:
        """Extract unique Go package paths from changed files."""
        packages = set()
        for f in files_changed:
            if f.endswith(".go") and not f.endswith("_test.go"):
                # Get the directory path as the package
                package = str(Path(f).parent)
                if package and package != ".":
                    packages.add(package)
            elif f.endswith("_test.go"):
                # Test file - get its package
                package = str(Path(f).parent)
                if package and package != ".":
                    packages.add(package)
        return list(packages)

    def _extract_python_test_paths(
        self, files_changed: list[str], repo_path: Path
    ) -> list[str]:
        """Extract relevant test paths from changed Python files."""
        test_paths = set()
        for f in files_changed:
            if not f.endswith(".py"):
                continue

            path = Path(f)
            # If it's a test file, add it directly
            if path.name.startswith("test_") or path.name.endswith("_test.py"):
                if (repo_path / f).exists():
                    test_paths.add(f)
            else:
                # For source files, try to find corresponding test file
                parent = path.parent
                test_file = parent / f"test_{path.name}"
                if (repo_path / test_file).exists():
                    test_paths.add(str(test_file))
                # Also check tests/ directory
                tests_file = Path("tests") / parent / f"test_{path.name}"
                if (repo_path / tests_file).exists():
                    test_paths.add(str(tests_file))

        return list(test_paths)

    def _get_test_env(self) -> dict[str, str]:
        """Get environment variables for test execution."""
        import os

        env = os.environ.copy()
        # Disable interactive prompts
        env["CI"] = "true"
        env["NONINTERACTIVE"] = "1"
        return env

    def _extract_failures(self, output: str, framework: TestFramework) -> list[str]:
        """Extract failed test names from output."""
        failures = []

        if framework == TestFramework.PYTEST:
            # Look for FAILED lines
            for line in output.split("\n"):
                if line.startswith("FAILED "):
                    test_name = line.replace("FAILED ", "").split(" ")[0]
                    failures.append(test_name)

        elif framework in [TestFramework.NPM, TestFramework.PNPM, TestFramework.YARN]:
            # Look for failing test patterns (jest/vitest)
            for line in output.split("\n"):
                if "FAIL " in line or "✕" in line or "✘" in line:
                    failures.append(line.strip())

        elif framework == TestFramework.GO:
            # Look for --- FAIL: lines
            for line in output.split("\n"):
                if line.startswith("--- FAIL:"):
                    test_name = line.replace("--- FAIL:", "").split(" ")[0].strip()
                    failures.append(test_name)

        elif framework == TestFramework.CARGO:
            # Look for test ... FAILED
            for line in output.split("\n"):
                if " FAILED" in line and "test " in line:
                    parts = line.split(" ")
                    for i, part in enumerate(parts):
                        if part == "test" and i + 1 < len(parts):
                            failures.append(parts[i + 1])
                            break

        return failures[:10]  # Limit to first 10 failures
