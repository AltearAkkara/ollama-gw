from __future__ import annotations

import logging
from dataclasses import dataclass
from typing import Dict, List, Optional, Tuple

import numpy as np
from ultralytics import YOLO

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)


# ---------------------------------------------------------------------------
# Detection result dataclass
# ---------------------------------------------------------------------------

@dataclass
class Detection:
    """Single YOLO detection result."""

    name:   str                       # YOLO class name
    cls_id: int
    conf:   float
    box:    Tuple[int, int, int, int]  # (x1, y1, x2, y2)
    xc:     int
    yc:     int


# ---------------------------------------------------------------------------
# YoloDetector
# ---------------------------------------------------------------------------

class YoloDetector:
    """Thin wrapper around the Ultralytics YOLO model for detection tasks."""

    def __init__(self, model_path: str, task: str = "detect"):
        self.model_path = model_path
        logger.info(f"Loading YOLO model from {model_path} (task={task})")
        self.model = YOLO(model_path, task=task)
        self.names: Dict[int, str] = self.model.names

    def detect(
        self,
        img: np.ndarray,
        conf_th: float = 0.5,
    ) -> List[Detection]:
        """Run inference on `img`; return all detections above `conf_th`."""
        results = self.model(img, device="cpu")[0]
        detections: List[Detection] = []

        for box in results.boxes:
            conf = float(box.conf[0])
            if conf < conf_th:
                continue

            cls_id = int(box.cls[0])
            name = self.names.get(cls_id, str(cls_id))

            x1, y1, x2, y2 = map(int, box.xyxy[0])
            detections.append(
                Detection(
                    name=name,
                    cls_id=cls_id,
                    conf=conf,
                    box=(x1, y1, x2, y2),
                    xc=(x1 + x2) // 2,
                    yc=(y1 + y2) // 2,
                )
            )

        return detections

    def get_best_by_categories(
        self,
        detections: List[Detection],
        categories: Dict[str, List[str]],
    ) -> Dict[str, Detection]:
        """Return the highest-confidence Detection per category; omits categories with no match."""
        best: Dict[str, Detection] = {}
        for category_name, allowed_names in categories.items():
            candidates = [d for d in detections if d.name in allowed_names]
            if candidates:
                best[category_name] = max(candidates, key=lambda d: d.conf)
        return best
