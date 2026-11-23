import argparse
import json
import os
import resource
import time
from collections import Counter
from pathlib import Path

from .audio import read_audio
from .decision import classify, summarize_scores
from .yamnet import Yamnet, windows


ROOT = Path(__file__).resolve().parents[1]


def analyze(path: Path, model_path: Path, labels_path: Path) -> dict:
    audio = read_audio(path)
    classifier = Yamnet(model_path, labels_path)
    counts = Counter()
    frames = []
    cpu_start, wall_start = time.process_time(), time.perf_counter()
    for index, frame in enumerate(windows(audio)):
        values = classifier.predict(frame)
        scores = summarize_scores(classifier.labels, values.tolist())
        decision = classify(scores)
        counts[decision.category] += 1
        frames.append({
            "index": index,
            "category": decision.category,
            "confidence": round(decision.confidence, 5),
            "speech": round(scores.speech, 5),
            "singing": round(scores.singing, 5),
            "music": round(scores.music, 5),
            "jingle": round(scores.jingle, 5),
            "target_gain": 1.0,
        })
    wall = time.perf_counter() - wall_start
    cpu = time.process_time() - cpu_start
    return {
        "input": str(path),
        "sample_rate": 16000,
        "analysis_only": True,
        "automatic_filter_enabled": False,
        "window_seconds": 0.975,
        "hop_seconds": round(15600 / 2 / 16000, 5),
        "frame_count": len(frames),
        "categories": dict(counts),
        "inference_wall_seconds": round(wall, 4),
        "inference_cpu_seconds": round(cpu, 4),
        "inference_cpu_percent": round(100 * cpu / wall, 1) if wall else 0.0,
        "peak_rss_kib": resource.getrusage(resource.RUSAGE_SELF).ru_maxrss,
        "frames": frames,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description="Analyze speech, singing, and music with YAMNet")
    parser.add_argument("input", type=Path)
    parser.add_argument("--model", type=Path, default=Path(os.getenv("YAMNET_MODEL", ROOT / "models/yamnet.tflite")))
    parser.add_argument("--labels", type=Path, default=Path(os.getenv("YAMNET_LABELS", ROOT / "models/yamnet_class_map.csv")))
    args = parser.parse_args()
    print(json.dumps(analyze(args.input, args.model, args.labels), indent=2))


if __name__ == "__main__":
    main()
