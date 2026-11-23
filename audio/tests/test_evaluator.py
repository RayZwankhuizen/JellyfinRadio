import importlib.util
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


SCRIPT = Path(os.environ["AUDIO_EVALUATOR"]) if "AUDIO_EVALUATOR" in os.environ else Path(__file__).resolve().parents[2] / "scripts" / "evaluate-audio.py"
SPEC = importlib.util.spec_from_file_location("audio_evaluator", SCRIPT)
evaluator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(evaluator)


class EvaluatorTests(unittest.TestCase):
    def test_bass_comparison_only_filters_the_second_analysis_upload(self):
        with tempfile.TemporaryDirectory() as directory:
            clip = Path(directory) / "clip.wav"
            clip.write_bytes(b"original")
            with patch.object(evaluator.subprocess, "run") as ffmpeg, patch.object(evaluator.urllib.request, "urlopen") as request:
                ffmpeg.return_value.stdout = b"analysis copy"
                request.return_value.__enter__.return_value.read.return_value = b'{"frames": []}'
                evaluator.analyze("http://localhost", clip, reduce_bass=False)
                evaluator.analyze("http://localhost", clip, reduce_bass=True)
                original, filtered = (call.args[0] for call in ffmpeg.call_args_list)
                self.assertNotIn("-af", original)
                self.assertIn("bass=f=150:g=-6", filtered)
                self.assertEqual(request.call_args.args[0].data, b"analysis copy")
            self.assertEqual(clip.read_bytes(), b"original")

    def test_maps_only_supported_model_outputs(self):
        self.assertEqual(evaluator.predict_clip([{"category": "music"}, {"category": "music"}]), "instrumental_music")
        self.assertEqual(evaluator.predict_clip([{"category": "speech"}]), "speech")
        self.assertEqual(evaluator.predict_clip([]), "uncertain")

    def test_manifest_rejects_same_source_in_multiple_splits(self):
        source = {
            "path": "clip.wav", "label": "speech", "source_id": "same-source",
            "license": "CC0-1.0", "attribution": "test fixture", "split": "train",
        }
        with tempfile.TemporaryDirectory() as directory:
            manifest = Path(directory) / "manifest.jsonl"
            manifest.write_text("\n".join(json.dumps({**source, "split": split}) for split in ("train", "test")), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "more than one split"):
                evaluator.read_manifest(manifest)

    def test_manifest_requires_provenance_and_known_label(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest = Path(directory) / "manifest.jsonl"
            manifest.write_text(json.dumps({"path": "clip.wav", "label": "speech"}), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "needs path"):
                evaluator.read_manifest(manifest)


if __name__ == "__main__":
    unittest.main()
