"""Copy the canonical contract into Go's embeddable package directory."""
from pathlib import Path

root = Path(__file__).resolve().parents[1]
destination = root / "backend/internal/api/assets/openapi.yaml"
destination.parent.mkdir(parents=True, exist_ok=True)
destination.write_bytes((root / "contracts/openapi.yaml").read_bytes())
print("Embedded OpenAPI updated")
