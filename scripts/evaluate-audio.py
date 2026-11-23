#!/usr/bin/env python3
import argparse
import json
import mimetypes
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "audio"))
from app.metrics import classification_report


LABELS = (
    "instrumental_music", "singing", "speech", "speech_over_music",
    "speech_over_singing", "advertisement", "news", "presenter",
)
PREDICTIONS = ("instrumental_music", "singing", "speech", "speech_over_music", "speech_over_singing", "uncertain")


def read_manifest(path):
    entries = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip() and not line.lstrip().startswith("#")]
    seen = {}
    for item in entries:
        required = ("path", "label", "source_id", "license", "attribution", "split")
        if any(not item.get(key) for key in required):
            raise ValueError(f"manifest entry needs {', '.join(required)}")
        if item["label"] not in LABELS:
            raise ValueError(f"unknown target label: {item['label']}")
        prior = seen.setdefault(item["source_id"], item["split"])
        if prior != item["split"]:
            raise ValueError(f"source {item['source_id']} appears in more than one split")
    return entries


def analyze(api, path, reduce_bass=None):
    payload = path.read_bytes()
    if not payload or len(payload) > 8 * 1024 * 1024:
        raise ValueError(f"{path}: clip must be between 1 byte and 8 MiB")
    content_type = mimetypes.guess_type(path.name)[0] or "application/octet-stream"
    if reduce_bass is not None:
        filters = ["-af", "bass=f=150:g=-6"] if reduce_bass else []
        result = subprocess.run([
            "ffmpeg", "-nostdin", "-v", "error", "-i", "pipe:0", "-t", "300",
            "-map", "0:a:0", "-ac", "1", "-ar", "16000", *filters,
            "-c:a", "pcm_s16le", "-f", "wav", "pipe:1",
        ], input=payload, capture_output=True, check=True, timeout=45)
        payload = result.stdout
        content_type = "audio/wav"
        if len(payload) > 8 * 1024 * 1024:
            raise ValueError(f"{path}: decoded clip exceeds 8 MiB; use a shorter excerpt")
    request = urllib.request.Request(
        api.rstrip("/") + "/api/analyze", data=payload,
        headers={"Content-Type": content_type},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=45) as response:
            return json.load(response)
    except urllib.error.URLError as error:
        raise RuntimeError(f"audio API request failed for {path}: {error}") from error


def predict_clip(frames):
    if not frames:
        return "uncertain"
    counts = {}
    for frame in frames:
        label = frame.get("category", "uncertain")
        if label == "music":
            label = "instrumental_music"
        if label not in PREDICTIONS:
            label = "uncertain"
        counts[label] = counts.get(label, 0) + 1
    return max(PREDICTIONS, key=lambda label: (counts.get(label, 0), label == "uncertain"))


def evaluate(api, manifest, entries, reduce_bass=None):
    expected, predicted, details = [], [], []
    singing_windows = {}
    for item in entries:
        path = (manifest.parent / item["path"]).resolve()
        result = analyze(api, path, reduce_bass)
        frames = result.get("frames", [])
        prediction = predict_clip(frames)
        windows = singing_windows.setdefault(item["label"], {"total": 0, "classified_as_singing": 0})
        windows["total"] += len(frames)
        windows["classified_as_singing"] += sum(frame.get("category") == "singing" for frame in frames)
        expected.append(item["label"])
        predicted.append(prediction)
        details.append({"path": item["path"], "truth": item["label"], "prediction": prediction, "frame_count": result.get("frame_count", 0)})
    report = classification_report(expected, predicted, LABELS, PREDICTIONS)
    report["clips"] = details
    report["singing_windows_by_true_label"] = singing_windows
    report["classifier"] = "current analysis API; clip prediction is the modal window label"
    report["note"] = "News, advertisement and presenter are user labels, not classes predicted by the current model."
    return report


def main():
    parser = argparse.ArgumentParser(description="Evaluate audio clip labels against the local analysis API")
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--api", default="http://127.0.0.1:8787")
    parser.add_argument("--compare-bass", action="store_true", help="repeat analysis with a -6 dB bass shelf at 150 Hz (requires FFmpeg)")
    args = parser.parse_args()
    entries = read_manifest(args.manifest)
    if args.compare_bass:
        report = {
            "original": evaluate(args.api, args.manifest, entries, reduce_bass=False),
            "bass_minus_6db": evaluate(args.api, args.manifest, entries, reduce_bass=True),
        }
    else:
        report = evaluate(args.api, args.manifest, entries)
    print(json.dumps(report, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
