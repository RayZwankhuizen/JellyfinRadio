import json
import subprocess
import sys
import tempfile
import urllib.request
from pathlib import Path

from app.metrics import classification_report


samples = Path(sys.argv[1])
endpoint = sys.argv[2].rstrip("/")


def get(path):
    with urllib.request.urlopen(endpoint + path, timeout=10) as response:
        return response.read()


assert get("/healthz").startswith(b"ok")
assert b"<title>Smart Radio" in get("/")
playlist = get("/stations.m3u")
assert playlist.startswith(b"#EXTM3U") and b"/stream/" in playlist
status = json.loads(get("/api/status"))
assert status["stations"] > 0, "Radio Browser importer returned no stations"

with tempfile.TemporaryDirectory() as directory:
    mixed = Path(directory) / "speech-over-music.wav"
    subprocess.run([
        "ffmpeg", "-nostdin", "-v", "error", "-y",
        "-i", str(samples / "speech.wav"), "-i", str(samples / "music.ogg"),
        "-filter_complex", "[0:a]volume=1[a];[1:a]volume=0.5[b];[a][b]amix=inputs=2:duration=shortest:normalize=0,aresample=16000,aformat=sample_fmts=s16:channel_layouts=mono",
        "-f", "wav", str(mixed),
    ], check=True)

    report = {"station_count": status["stations"], "cases": {}}
    expected, predicted = [], []
    for name, path in {
        "speech": samples / "speech.wav",
        "singing": samples / "singing.wav",
        "music": samples / "music.ogg",
        "speech_over_music": mixed,
    }.items():
        request = urllib.request.Request(endpoint + "/api/analyze", data=path.read_bytes(), method="POST", headers={"Content-Type": "audio/wav"})
        with urllib.request.urlopen(request, timeout=60) as response:
            result = json.load(response)
        assert result["analysis_only"] and not result["automatic_filter_enabled"]
        assert all(frame["target_gain"] == 1.0 for frame in result["frames"])
        if name == "singing":
            assert all(frame["category"] != "speech" for frame in result["frames"]), "singing was classified as speech"
        if name == "music":
            assert all(frame["category"] != "speech" for frame in result["frames"]), "instrumental music was classified as speech"
        categories = {}
        for frame in result["frames"]:
            categories[frame["category"]] = categories.get(frame["category"], 0) + 1
        report["cases"][name] = {
            "frames": result["frame_count"],
            "categories": categories,
            "cpu_seconds": result["inference_cpu_seconds"],
            "peak_rss_mib": round(result["peak_rss_kib"] / 1024, 1),
            "all_target_gains_unity": True,
        }
        expected.append({"speech": "speech", "singing": "singing", "music": "instrumental_music", "speech_over_music": "speech_over_music"}[name])
        prediction = max(categories, key=lambda label: (categories[label], label == "uncertain")) if categories else "uncertain"
        predicted.append("instrumental_music" if prediction == "music" else prediction)
    report["baseline_metrics"] = classification_report(
        expected, predicted,
        ["speech", "singing", "instrumental_music", "speech_over_music"],
        ["speech", "singing", "instrumental_music", "speech_over_music", "uncertain"],
    )
    print(json.dumps(report, indent=2))
