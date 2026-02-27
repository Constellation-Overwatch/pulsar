#!/usr/bin/env python3
"""Export YOLO26 model to ONNX format for Pulsar detector.

Usage:
    pip install ultralytics
    python scripts/export_yolo26.py [variant]

Variants: yolo26n (nano), yolo26s (small, default), yolo26m, yolo26l, yolo26x

The exported ONNX model uses the end-to-end (NMS-free) head by default:
    Input:  images   (1, 3, 640, 640) float32
    Output: output0  (1, 300, 6)      float32  [x1, y1, x2, y2, score, class_id]
"""
import sys
import shutil
from pathlib import Path

def main():
    try:
        from ultralytics import YOLO
    except ImportError:
        print("Error: ultralytics not installed. Run: pip install ultralytics")
        sys.exit(1)

    variant = sys.argv[1] if len(sys.argv) > 1 else "yolo26s"
    if not variant.startswith("yolo26"):
        variant = f"yolo26{variant}"

    print(f"Loading {variant}.pt (downloads automatically if not cached)...")
    model = YOLO(f"{variant}.pt")

    print(f"Exporting to ONNX (imgsz=640, e2e head)...")
    onnx_path = model.export(format="onnx", imgsz=640)
    print(f"Exported: {onnx_path}")

    # Copy to data/ directory
    dest = Path(__file__).parent.parent / "data" / f"{variant}.onnx"
    dest.parent.mkdir(exist_ok=True)
    shutil.copy2(onnx_path, dest)
    print(f"Copied to: {dest}")
    print(f"\nSet MODEL_PATH={dest.relative_to(Path(__file__).parent.parent)} in your .env")

if __name__ == "__main__":
    main()
