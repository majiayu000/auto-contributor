"""Docker container-based test runner for isolated environments."""

import asyncio
import hashlib
import json
import shutil
import tempfile
from dataclasses import dataclass, field
from enum import Enum
from pathlib import Path

import structlog

from auto_contributor.runner.test_runner import TestFramework, TestResult

logger = structlog.get_logger(__name__)


class ContainerRuntime(str, Enum):
    """Supported container runtimes."""

    DOCKER = "docker"
    PODMAN = "podman"
    ORBSTACK = "orbstack"  # Uses docker CLI


@dataclass
class ContainerConfig:
    """Configuration for container-based testing."""

    enabled: bool = True
    runtime: ContainerRuntime = ContainerRuntime.DOCKER
    memory_limit: str = "4g"
    cpu_limit: str = "2"
    timeout: int = 600  # 10 minutes
    cache_dependencies: bool = True
    cache_dir: Path = field(default_factory=lambda: Path.home() / ".auto-contributor" / "cache")


# Pre-built images for each language with common dependencies
LANGUAGE_IMAGES = {
    "python": "python:3.11-slim",
    "go": "golang:1.22-alpine",
    "rust": "rust:1.75-slim",
    "javascript": "node:20-slim",
    "typescript": "node:20-slim",
    "java": "eclipse-temurin:21-jdk",
    "kotlin": "eclipse-temurin:21-jdk",
}

# Install scripts for each language
INSTALL_SCRIPTS = {
    "python": """
set -e
echo "=== Installing Python dependencies ==="

# Install build dependencies for native packages
apt-get update -qq && apt-get install -y -qq git build-essential > /dev/null 2>&1 || true

# Upgrade pip
pip install --upgrade pip --quiet

# Install dependencies based on project type
if [ -f pyproject.toml ]; then
    echo "Found pyproject.toml, installing with pip install -e ."
    pip install -e ".[dev,test]" --quiet 2>/dev/null || \
    pip install -e ".[dev]" --quiet 2>/dev/null || \
    pip install -e ".[test]" --quiet 2>/dev/null || \
    pip install -e . --quiet 2>/dev/null || \
    echo "Warning: pip install -e . failed, trying requirements.txt"
fi

if [ -f requirements.txt ]; then
    echo "Installing from requirements.txt"
    pip install -r requirements.txt --quiet
fi

if [ -f requirements-dev.txt ]; then
    pip install -r requirements-dev.txt --quiet
fi

if [ -f requirements-test.txt ]; then
    pip install -r requirements-test.txt --quiet
fi

if [ -f test-requirements.txt ]; then
    pip install -r test-requirements.txt --quiet
fi

# Install pytest if not already installed
pip install pytest pytest-cov --quiet 2>/dev/null || true

echo "=== Dependencies installed ==="
""",
    "go": """
set -e
echo "=== Installing Go dependencies ==="

# Install git for go get
apk add --no-cache git > /dev/null 2>&1 || apt-get update -qq && apt-get install -y -qq git > /dev/null 2>&1

# Download dependencies
if [ -f go.mod ]; then
    go mod download
    go mod tidy
fi

echo "=== Dependencies installed ==="
""",
    "rust": """
set -e
echo "=== Installing Rust dependencies ==="

# Install build dependencies
apt-get update -qq && apt-get install -y -qq git build-essential pkg-config libssl-dev > /dev/null 2>&1 || true

# Build dependencies
if [ -f Cargo.toml ]; then
    cargo fetch
    cargo build --release 2>/dev/null || cargo build
fi

echo "=== Dependencies installed ==="
""",
    "javascript": """
set -e
echo "=== Installing JavaScript dependencies ==="

# Install git
apt-get update -qq && apt-get install -y -qq git > /dev/null 2>&1 || true

# Install dependencies based on lock file
if [ -f pnpm-lock.yaml ]; then
    npm install -g pnpm --quiet
    pnpm install --frozen-lockfile 2>/dev/null || pnpm install
elif [ -f yarn.lock ]; then
    npm install -g yarn --quiet
    yarn install --frozen-lockfile 2>/dev/null || yarn install
elif [ -f package-lock.json ]; then
    npm ci 2>/dev/null || npm install
elif [ -f package.json ]; then
    npm install
fi

echo "=== Dependencies installed ==="
""",
    "typescript": """
set -e
echo "=== Installing TypeScript dependencies ==="

# Same as JavaScript
apt-get update -qq && apt-get install -y -qq git > /dev/null 2>&1 || true

if [ -f pnpm-lock.yaml ]; then
    npm install -g pnpm --quiet
    pnpm install --frozen-lockfile 2>/dev/null || pnpm install
elif [ -f yarn.lock ]; then
    npm install -g yarn --quiet
    yarn install --frozen-lockfile 2>/dev/null || yarn install
elif [ -f package-lock.json ]; then
    npm ci 2>/dev/null || npm install
elif [ -f package.json ]; then
    npm install
fi

echo "=== Dependencies installed ==="
""",
}

# Test commands for each language
TEST_COMMANDS = {
    "python": """
echo "=== Running Python tests ==="
if [ -n "$TEST_PATHS" ]; then
    python -m pytest $TEST_PATHS -x --tb=short -q
else
    python -m pytest -x --tb=short -q
fi
""",
    "go": """
echo "=== Running Go tests ==="
if [ -n "$TEST_PATHS" ]; then
    go test -v -timeout 180s $TEST_PATHS
else
    go test -v -timeout 180s ./...
fi
""",
    "rust": """
echo "=== Running Rust tests ==="
cargo test -- --test-threads=1
""",
    "javascript": """
echo "=== Running JavaScript tests ==="
if [ -f pnpm-lock.yaml ]; then
    pnpm test --passWithNoTests 2>/dev/null || pnpm test
elif [ -f yarn.lock ]; then
    yarn test --passWithNoTests 2>/dev/null || yarn test
else
    npm test -- --passWithNoTests 2>/dev/null || npm test
fi
""",
    "typescript": """
echo "=== Running TypeScript tests ==="
if [ -f pnpm-lock.yaml ]; then
    pnpm test --passWithNoTests 2>/dev/null || pnpm test
elif [ -f yarn.lock ]; then
    yarn test --passWithNoTests 2>/dev/null || yarn test
else
    npm test -- --passWithNoTests 2>/dev/null || npm test
fi
""",
}


class ContainerTestRunner:
    """Runs tests in isolated Docker containers."""

    def __init__(self, config: ContainerConfig | None = None):
        self.config = config or ContainerConfig()
        self.config.cache_dir.mkdir(parents=True, exist_ok=True)
        self._runtime_path: str | None = None

    async def _get_runtime(self) -> str:
        """Get the container runtime executable path."""
        if self._runtime_path:
            return self._runtime_path

        # Try different runtimes
        for runtime in ["docker", "podman"]:
            path = shutil.which(runtime)
            if path:
                self._runtime_path = path
                logger.info("container_runtime_found", runtime=runtime)
                return path

        raise RuntimeError("No container runtime found. Please install Docker or Podman.")

    def _detect_language(self, repo_path: Path) -> str | None:
        """Detect the primary language of the repository."""
        indicators = {
            "python": ["pyproject.toml", "setup.py", "requirements.txt", "Pipfile"],
            "go": ["go.mod", "go.sum"],
            "rust": ["Cargo.toml"],
            "javascript": ["package.json"],
            "typescript": ["tsconfig.json"],
            "java": ["pom.xml", "build.gradle"],
        }

        # Check for TypeScript first (it also has package.json)
        if (repo_path / "tsconfig.json").exists():
            return "typescript"

        for lang, files in indicators.items():
            for f in files:
                if (repo_path / f).exists():
                    return lang

        return None

    def _get_cache_volume_name(self, language: str, repo_path: Path) -> str:
        """Generate a cache volume name based on language and repo."""
        # Create a hash of the repo path for uniqueness
        repo_hash = hashlib.md5(str(repo_path).encode()).hexdigest()[:8]
        return f"auto-contributor-cache-{language}-{repo_hash}"

    def _get_test_paths(self, language: str, files_changed: list[str]) -> str:
        """Get test paths based on changed files."""
        if not files_changed:
            return ""

        if language == "python":
            test_paths = set()
            for f in files_changed:
                if f.endswith(".py"):
                    path = Path(f)
                    # If it's a test file, add it directly
                    if path.name.startswith("test_") or path.name.endswith("_test.py"):
                        test_paths.add(f)
                    else:
                        # Try to find corresponding test file
                        test_file = path.parent / f"test_{path.name}"
                        test_paths.add(str(test_file))
                        # Also try tests/ directory
                        tests_file = Path("tests") / path.parent / f"test_{path.name}"
                        test_paths.add(str(tests_file))
            return " ".join(test_paths) if test_paths else ""

        elif language == "go":
            packages = set()
            for f in files_changed:
                if f.endswith(".go"):
                    package = str(Path(f).parent)
                    if package and package != ".":
                        packages.add(f"./{package}/...")
            return " ".join(packages) if packages else ""

        return ""

    async def run_tests(
        self,
        repo_path: Path,
        files_changed: list[str] | None = None,
        language: str | None = None,
    ) -> TestResult:
        """
        Run tests in an isolated Docker container.

        Args:
            repo_path: Path to the repository
            files_changed: List of changed files (for targeted testing)
            language: Override language detection

        Returns:
            TestResult with pass/fail status and output
        """
        # Detect language if not provided
        detected_language = language or self._detect_language(repo_path)
        if not detected_language:
            logger.warning("language_not_detected", path=str(repo_path))
            return TestResult(
                passed=True,
                framework=TestFramework.UNKNOWN,
                output="Could not detect project language",
                duration=0.0,
            )

        if detected_language not in LANGUAGE_IMAGES:
            logger.warning("unsupported_language", language=detected_language)
            return TestResult(
                passed=True,
                framework=TestFramework.UNKNOWN,
                output=f"Unsupported language: {detected_language}",
                duration=0.0,
            )

        logger.info(
            "running_container_tests",
            language=detected_language,
            repo=str(repo_path),
            files_changed=len(files_changed) if files_changed else 0,
        )

        try:
            runtime = await self._get_runtime()
            result = await self._run_in_container(
                runtime=runtime,
                repo_path=repo_path,
                language=detected_language,
                files_changed=files_changed or [],
            )
            return result

        except asyncio.TimeoutError:
            return TestResult(
                passed=False,
                framework=self._get_framework(detected_language),
                output=f"Container test timed out after {self.config.timeout} seconds",
                duration=float(self.config.timeout),
                exit_code=-1,
            )
        except Exception as e:
            logger.error("container_test_failed", error=str(e))
            return TestResult(
                passed=False,
                framework=self._get_framework(detected_language),
                output=f"Container test error: {str(e)}",
                duration=0.0,
                exit_code=-1,
            )

    async def _run_in_container(
        self,
        runtime: str,
        repo_path: Path,
        language: str,
        files_changed: list[str],
    ) -> TestResult:
        """Execute tests inside a Docker container."""
        import time

        start_time = time.time()

        image = LANGUAGE_IMAGES[language]
        install_script = INSTALL_SCRIPTS.get(language, "echo 'No install script'")
        test_script = TEST_COMMANDS.get(language, "echo 'No test command'")
        test_paths = self._get_test_paths(language, files_changed)

        # Create combined script
        full_script = f"""#!/bin/sh
set -e

cd /workspace

# Set test paths environment variable
export TEST_PATHS="{test_paths}"

# Install phase
{install_script}

# Test phase
{test_script}

echo "=== Tests completed successfully ==="
"""

        # Write script to temp file
        with tempfile.NamedTemporaryFile(mode="w", suffix=".sh", delete=False) as f:
            f.write(full_script)
            script_path = f.name

        try:
            # Build docker run command
            cmd = [
                runtime,
                "run",
                "--rm",  # Remove container after exit
                f"--memory={self.config.memory_limit}",
                f"--cpus={self.config.cpu_limit}",
                "--network=host",  # Allow network for dependency downloads
                "-v", f"{repo_path}:/workspace:rw",
                "-v", f"{script_path}:/run_tests.sh:ro",
                "-w", "/workspace",
                "-e", "CI=true",
                "-e", "NONINTERACTIVE=1",
            ]

            # Add cache volume for dependencies
            if self.config.cache_dependencies:
                cache_volume = self._get_cache_volume_name(language, repo_path)
                cache_mount = self._get_cache_mount(language)
                if cache_mount:
                    cmd.extend(["-v", f"{cache_volume}:{cache_mount}"])

            # Add image and command
            cmd.extend([
                image,
                "sh", "/run_tests.sh",
            ])

            logger.info(
                "executing_container",
                image=image,
                language=language,
                test_paths=test_paths or "all",
            )

            # Run the container
            process = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.STDOUT,
            )

            try:
                stdout, _ = await asyncio.wait_for(
                    process.communicate(),
                    timeout=self.config.timeout,
                )
            except asyncio.TimeoutError:
                # Kill the container
                container_id = await self._get_running_container(runtime, repo_path)
                if container_id:
                    await asyncio.create_subprocess_exec(
                        runtime, "kill", container_id,
                        stdout=asyncio.subprocess.DEVNULL,
                        stderr=asyncio.subprocess.DEVNULL,
                    )
                raise

            duration = time.time() - start_time
            output = stdout.decode(errors="replace")
            passed = process.returncode == 0

            logger.info(
                "container_test_complete",
                passed=passed,
                duration=f"{duration:.2f}s",
                exit_code=process.returncode,
            )

            return TestResult(
                passed=passed,
                framework=self._get_framework(language),
                output=output,
                duration=duration,
                failed_tests=self._extract_failures(output, language) if not passed else [],
                exit_code=process.returncode or 0,
            )

        finally:
            # Clean up temp script
            Path(script_path).unlink(missing_ok=True)

    async def _get_running_container(self, runtime: str, repo_path: Path) -> str | None:
        """Get the ID of a running container for this repo."""
        # This is a best-effort approach
        cmd = [runtime, "ps", "-q", "--filter", f"volume={repo_path}"]
        process = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
        stdout, _ = await process.communicate()
        container_id = stdout.decode().strip()
        return container_id if container_id else None

    def _get_framework(self, language: str) -> TestFramework:
        """Get the test framework for a language."""
        mapping = {
            "python": TestFramework.PYTEST,
            "go": TestFramework.GO,
            "rust": TestFramework.CARGO,
            "javascript": TestFramework.NPM,
            "typescript": TestFramework.NPM,
        }
        return mapping.get(language, TestFramework.UNKNOWN)

    def _get_cache_mount(self, language: str) -> str | None:
        """Get the cache mount path for a language."""
        cache_paths = {
            "python": "/root/.cache/pip",
            "go": "/go/pkg/mod",
            "rust": "/usr/local/cargo/registry",
            "javascript": "/root/.npm",
            "typescript": "/root/.npm",
        }
        return cache_paths.get(language)

    def _extract_failures(self, output: str, language: str) -> list[str]:
        """Extract failed test names from output."""
        failures = []

        if language == "python":
            for line in output.split("\n"):
                if line.startswith("FAILED "):
                    test_name = line.replace("FAILED ", "").split(" ")[0]
                    failures.append(test_name)

        elif language == "go":
            for line in output.split("\n"):
                if line.startswith("--- FAIL:"):
                    test_name = line.replace("--- FAIL:", "").split(" ")[0].strip()
                    failures.append(test_name)

        elif language == "rust":
            for line in output.split("\n"):
                if " FAILED" in line and "test " in line:
                    parts = line.split(" ")
                    for i, part in enumerate(parts):
                        if part == "test" and i + 1 < len(parts):
                            failures.append(parts[i + 1])
                            break

        elif language in ["javascript", "typescript"]:
            for line in output.split("\n"):
                if "FAIL " in line or "✕" in line or "✘" in line:
                    failures.append(line.strip())

        return failures[:10]  # Limit to first 10 failures

    async def cleanup_cache(self, older_than_days: int = 7) -> None:
        """Clean up old cache volumes."""
        try:
            runtime = await self._get_runtime()

            # List volumes matching our pattern
            cmd = [
                runtime, "volume", "ls",
                "--filter", "name=auto-contributor-cache",
                "--format", "{{.Name}}",
            ]

            process = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.DEVNULL,
            )
            stdout, _ = await process.communicate()

            volumes = stdout.decode().strip().split("\n")
            logger.info("found_cache_volumes", count=len(volumes))

            # For now, just log - actual cleanup would need more logic
            # to track volume age
            for volume in volumes:
                if volume:
                    logger.debug("cache_volume", name=volume)

        except Exception as e:
            logger.warning("cache_cleanup_failed", error=str(e))

    async def is_available(self) -> bool:
        """Check if container runtime is available."""
        try:
            runtime = await self._get_runtime()
            process = await asyncio.create_subprocess_exec(
                runtime, "version",
                stdout=asyncio.subprocess.DEVNULL,
                stderr=asyncio.subprocess.DEVNULL,
            )
            await process.communicate()
            return process.returncode == 0
        except Exception:
            return False


# Convenience function for simple usage
async def run_tests_in_container(
    repo_path: Path,
    files_changed: list[str] | None = None,
    language: str | None = None,
    config: ContainerConfig | None = None,
) -> TestResult:
    """
    Run tests in an isolated Docker container.

    This is a convenience function that creates a ContainerTestRunner
    and runs tests.

    Args:
        repo_path: Path to the repository
        files_changed: Optional list of changed files for targeted testing
        language: Optional language override
        config: Optional container configuration

    Returns:
        TestResult with pass/fail status
    """
    runner = ContainerTestRunner(config)
    return await runner.run_tests(repo_path, files_changed, language)
