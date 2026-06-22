"""Crop-only test: run the YOLO crop model on one image OR a whole folder,
draw boxes, save result(s).

Usage:
    # single image (legacy, still works)
    python test_crop_only.py --image path/to/img.jpg

    # whole folder (recursive)
    python test_crop_only.py --dir path/to/folder

    # accepted extensions are .jpg .jpeg .png by default
    python test_crop_only.py --dir path/to/folder --exts jpg,png,bmp

Common options:
    [--conf 0.1] [--max_det 0]
    [--filter z_1_level_gauge,dial]
    [--out_dir crop_results]
    [--save_crops]
    [--recursive]

When --dir is supplied, every annotated image and (optional) per-detection
crop is written under --out_dir, mirroring the source filenames.
"""
from __future__ import annotations

import argparse
import logging
import os
import sys
from glob import glob
from typing import Iterable, List

import cv2

from yolo_detector import YoloDetector, Detection
# from gauge_det.dial.dial_cropper import DEFAULT_CROP_MODEL

CURRENT_DIR = os.path.dirname(os.path.abspath(__file__))
DEFAULT_CROP_MODEL = os.path.join(CURRENT_DIR, "models", "best_crop_egat_23.pt")
# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _color_for(name: str) -> tuple[int, int, int]:
    """Stable colour per class name (BGR)."""
    h = abs(hash(name)) % 0xFFFFFF
    return (h & 0xFF, (h >> 8) & 0xFF, (h >> 16) & 0xFF)


def _gather_images(directory: str, exts: Iterable[str], recursive: bool) -> List[str]:
    """Return sorted absolute paths to every image under *directory*."""
    files: List[str] = []
    for ext in exts:
        ext_clean = ext.lstrip(".").lower()
        pattern = "**/*." + ext_clean if recursive else "*." + ext_clean
        files.extend(glob(os.path.join(directory, pattern), recursive=recursive))
        # Pick up uppercase extensions too without depending on the FS.
        pattern_upper = "**/*." + ext_clean.upper() if recursive else "*." + ext_clean.upper()
        files.extend(glob(os.path.join(directory, pattern_upper), recursive=recursive))
    return sorted(set(files))


def _draw_detections(img, detections: List[Detection]):
    out = img.copy()
    for i, d in enumerate(detections):
        x1, y1, x2, y2 = d.box
        col = _color_for(d.name)
        cv2.rectangle(out, (x1, y1), (x2, y2), col, 2, cv2.LINE_AA)
        label = f"{i}: {d.name} {d.conf:.2f}"
        (tw, th), bl = cv2.getTextSize(label, cv2.FONT_HERSHEY_SIMPLEX, 0.55, 1)
        cv2.rectangle(out, (x1, max(0, y1 - th - bl - 4)), (x1 + tw + 4, y1), col, -1)
        text_col = (0, 0, 0) if sum(col) > 400 else (255, 255, 255)
        cv2.putText(out, label, (x1 + 2, y1 - bl - 2),
                    cv2.FONT_HERSHEY_SIMPLEX, 0.55, text_col, 1, cv2.LINE_AA)
    return out


def _process_one(
    detector: YoloDetector,
    img_path: str,
    out_dir: str,
    conf_th: float,
    max_det: int,
    keep: set,
    save_crops: bool,
    save_annotated: bool,
    src_root: str | None,
) -> tuple[int, int]:
    """Run detection on one image. Returns ``(num_kept, num_total)``."""
    img = cv2.imread(img_path)
    if img is None:
        print(f"[skip] cannot read: {img_path}")
        return (0, 0)
    h, w = img.shape[:2]

    detections = detector.detect(img, conf_th=conf_th)
    total = len(detections)
    if keep:
        detections = [d for d in detections if d.name in keep]
    detections.sort(key=lambda d: d.conf, reverse=True)
    if max_det > 0:
        detections = detections[: max_det]

    rel = os.path.relpath(img_path, src_root) if src_root else os.path.basename(img_path)
    rel_no_ext = os.path.splitext(rel)[0]

    print(f"\n{rel}  ({w}x{h})  total={total}  kept={len(detections)}")
    if detections:
        for i, d in enumerate(detections):
            x1, y1, x2, y2 = d.box
            print(f"{d.name:<28}")
            print(f"  [{i:>2}] {d.name:<28}  conf={d.conf:.2f}  box=({x1},{y1},{x2},{y2})")
            if save_crops and d.name == "z_3_counter":
                crop = img[y1:y2, x1:x2]
                crop_path = os.path.join(out_dir, rel_no_ext + f"_crop{i}_{d.name}.jpg")
                os.makedirs(os.path.dirname(crop_path) or out_dir, exist_ok=True)
                cv2.imwrite(crop_path, crop)
    else:
        print("  (no detections after filter / max_det)")

    if save_annotated:
        out_path = os.path.join(out_dir, rel_no_ext + "_crops.jpg")
        os.makedirs(os.path.dirname(out_path) or out_dir, exist_ok=True)
        annotated = _draw_detections(img, detections)
        cv2.imwrite(out_path, annotated)

    return (len(detections), total)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> None:
    p = argparse.ArgumentParser(description="Test the crop YOLO model on one image or a folder.")
    src = p.add_mutually_exclusive_group(required=True)
    src.add_argument("--image", help="Path to a single input image")
    src.add_argument("--dir",   help="Path to a folder of input images")

    p.add_argument("--exts", default="jpg,jpeg,png", help="Comma-separated extensions to include when --dir is used (default: jpg,jpeg,png)")
    p.add_argument("--recursive", action="store_true", help="Recurse into subdirectories when --dir is used")

    p.add_argument("--model",   default=DEFAULT_CROP_MODEL, help="Path to YOLO crop model (.pt or .onnx)")
    p.add_argument("--conf",    type=float, default=0.1, help="Confidence threshold (default 0.1)")
    p.add_argument("--max_det", type=int,   default=0,   help="Max detections kept per image (0 = all). Sorted by confidence.")
    p.add_argument("--filter",  default="", help="Comma-separated class names to keep, e.g. 'z_1_level_gauge,dial'")

    p.add_argument("--out_dir", default="crop_results", help="Directory to write annotated images / per-detection crops (default: crop_results)")
    p.add_argument("--save_crops", action="store_true",  help="Also save each detection as a separate JPEG")
    p.add_argument("--no_annotated", action="store_true", help="Skip the annotated <stem>_crops.jpg image (use with --save_crops to get only the raw crops)")

    # Legacy single-image-only flag, kept for backward compatibility.
    p.add_argument("--out", default=None, help="(single-image mode only) Path for the annotated image. Overrides --out_dir.")

    args = p.parse_args()

    logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(name)s: %(message)s")
    keep = {n.strip() for n in args.filter.split(",") if n.strip()}
    exts = [e.strip().lstrip(".") for e in args.exts.split(",") if e.strip()]

    print(f"Model:     {args.model}")
    print(f"conf_th:   {args.conf}")
    print(f"max_det:   {args.max_det}")
    print(f"filter:    {args.filter or '(none)'}")
    if args.dir:
        print(f"input:     dir = {args.dir}  exts={exts}  recursive={args.recursive}")
    else:
        print(f"input:     image = {args.image}")
    print(f"out_dir:   {args.out_dir}")
    print(f"save_crops: {args.save_crops}")
    print(f"save_annotated: {not args.no_annotated}")

    # Sanity nudge: if neither output is requested, nothing useful gets written.
    if args.no_annotated and not args.save_crops:
        print("\n[warn] --no_annotated without --save_crops means no images will be written.")

    detector = YoloDetector(args.model, task="detect")

    # ---------------- single image path ----------------
    if args.image:
        if not os.path.isfile(args.image):
            print(f"[ERROR] No such file: {args.image}")
            sys.exit(1)
        # Honour the legacy --out flag when given.
        if args.out:
            out_dir = os.path.dirname(os.path.abspath(args.out)) or "."
            os.makedirs(out_dir, exist_ok=True)
            kept, total = _process_one(
                detector, args.image, out_dir, args.conf, args.max_det, keep,
                args.save_crops, save_annotated=not args.no_annotated,
                src_root=os.path.dirname(args.image) or ".",
            )
            # rename the auto-generated annotated file to match --out
            if not args.no_annotated:
                stem = os.path.splitext(os.path.basename(args.image))[0]
                auto = os.path.join(out_dir, stem + "_crops.jpg")
                if os.path.abspath(auto) != os.path.abspath(args.out) and os.path.isfile(auto):
                    os.replace(auto, args.out)
                print(f"\nAnnotated image -> {args.out}")
        else:
            os.makedirs(args.out_dir, exist_ok=True)
            kept, total = _process_one(
                detector, args.image, args.out_dir, args.conf, args.max_det, keep,
                args.save_crops, save_annotated=not args.no_annotated,
                src_root=os.path.dirname(args.image) or ".",
            )
        print(f"\nDone. {kept}/{total} detections kept.")
        return

    # ---------------- folder path ----------------
    if not os.path.isdir(args.dir):
        print(f"[ERROR] No such directory: {args.dir}")
        sys.exit(1)

    files = _gather_images(args.dir, exts, args.recursive)
    if not files:
        print(f"[ERROR] No images matching {exts} in {args.dir}")
        sys.exit(1)

    print(f"Found {len(files)} image(s).")
    os.makedirs(args.out_dir, exist_ok=True)

    grand_kept = grand_total = 0
    n_with_dets = 0
    for path in files:
        kept, total = _process_one(
            detector, path, args.out_dir, args.conf, args.max_det, keep,
            args.save_crops, save_annotated=not args.no_annotated,
            src_root=args.dir,
        )
        grand_kept += kept
        grand_total += total
        if kept > 0:
            n_with_dets += 1

    print("\n" + "=" * 60)
    print(f"Processed {len(files)} image(s)")
    print(f"  with detections: {n_with_dets}")
    print(f"  kept / total   : {grand_kept} / {grand_total}")
    print(f"Outputs saved under: {args.out_dir}")


if __name__ == "__main__":
    main()
