import subprocess
from pathlib import Path

import pytest

STUDIO = Path(__file__).resolve().parents[2]


@pytest.fixture(scope="session")
def synth_dir(tmp_path_factory) -> Path:
    """합성 데이터(시드 7). 생성기는 라이브러리로 가져오지 않고 따로 실행한다."""
    out = tmp_path_factory.mktemp("synth")
    subprocess.run(["uv", "run", "--quiet", "--project", str(STUDIO / "synth"), "python", "-m", "studio_synth", "--seed", "7",
                    "--out", str(out), "--observed-at", "2026-10-28"], check=True, cwd=STUDIO / "synth")
    return out
