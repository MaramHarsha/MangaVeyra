"""Allow ``python -m mangaveyra_worker``."""

from mangaveyra_worker.cli import main

if __name__ == "__main__":  # pragma: no cover - thin entry point
    raise SystemExit(main())
