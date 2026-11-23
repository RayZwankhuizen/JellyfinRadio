import tempfile
import unittest
import wave
from pathlib import Path

import numpy as np

from app.audio import read_audio, read_audio_bytes
from app.yamnet import windows


class AudioTests(unittest.TestCase):
    def test_read_wave_downmixes_and_resamples(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "stereo.wav"
            frames = np.full((8000, 2), 8192, dtype="<i2")
            with wave.open(str(path), "wb") as audio:
                audio.setnchannels(2)
                audio.setsampwidth(2)
                audio.setframerate(8000)
                audio.writeframes(frames.tobytes())
            samples = read_audio(path)
        self.assertEqual(samples.size, 16000)
        self.assertAlmostEqual(float(samples.mean()), 0.25, places=3)

    def test_windows_are_half_overlapped_and_include_the_tail(self):
        samples = np.zeros(16000 * 2, dtype=np.float32)
        frames = list(windows(samples))
        self.assertEqual(len(frames), 4)
        self.assertEqual([len(frame) for frame in frames], [15600, 15600, 15600, 8600])

    def test_read_audio_bytes_decodes_without_a_temporary_file(self):
        import io

        output = io.BytesIO()
        with wave.open(output, "wb") as audio:
            audio.setnchannels(1)
            audio.setsampwidth(2)
            audio.setframerate(16000)
            audio.writeframes(np.zeros(15600, dtype="<i2").tobytes())
        samples = read_audio_bytes(output.getvalue())
        self.assertEqual(samples.size, 15600)

    def test_audio_upload_size_is_bounded(self):
        with self.assertRaisesRegex(ValueError, "8 MiB"):
            read_audio_bytes(b"x" * (8 * 1024 * 1024 + 1))

    def test_empty_and_long_audio_are_rejected(self):
        from app.audio import _bound_analysis_audio

        with self.assertRaisesRegex(ValueError, "no samples"):
            _bound_analysis_audio(np.zeros(0, dtype=np.float32))
        with self.assertRaisesRegex(ValueError, "5 minutes"):
            _bound_analysis_audio(np.zeros(16000 * 301, dtype=np.float32))


if __name__ == "__main__":
    unittest.main()
