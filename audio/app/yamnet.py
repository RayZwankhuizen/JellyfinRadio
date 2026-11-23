import csv
from pathlib import Path

import numpy as np
from ai_edge_litert.compiled_model import CompiledModel, HardwareAccelerator


SAMPLE_RATE = 16_000
WINDOW_SAMPLES = 15_600  # Official YAMNet TFLite signature: 0.975 seconds.


class Yamnet:
    def __init__(self, model_path: Path, labels_path: Path):
        self.labels = []
        with labels_path.open(newline="", encoding="utf-8") as file:
            self.labels = [row["display_name"] for row in csv.DictReader(file)]
        if len(self.labels) != 521:
            raise ValueError(f"expected 521 YAMNet labels, found {len(self.labels)}")
        self.model = CompiledModel.from_file(str(model_path), HardwareAccelerator.CPU)
        self.inputs = self.model.create_input_buffers(0)
        self.outputs = self.model.create_output_buffers(0)

    def predict(self, samples: np.ndarray) -> np.ndarray:
        frame = np.zeros(WINDOW_SAMPLES, dtype=np.float32)
        count = min(samples.size, WINDOW_SAMPLES)
        frame[:count] = samples[:count]
        self.inputs[0].write(frame)
        self.model.run_by_index(0, self.inputs, self.outputs)
        return self.outputs[0].read(521, np.float32).reshape(-1)


def windows(samples: np.ndarray, hop_samples: int = WINDOW_SAMPLES // 2):
    if hop_samples < 1:
        raise ValueError("hop size must be positive")
    if samples.size == 0:
        return
    starts = range(0, samples.size, hop_samples)
    for start in starts:
        if start and samples.size - start < hop_samples:
            break
        yield samples[start:start + WINDOW_SAMPLES]
