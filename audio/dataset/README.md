# Audio evaluation dataset

`manifest.example.jsonl` defines the target labels and provenance fields. It contains placeholder paths and recording metadata, not audio, so it is a schema example rather than a completed/reproducible dataset. The two public CC0 references below were not downloaded into this checkout. Only add material with a verified reuse license or recordings made by the project owner with consent. Record the upstream source, license and attribution for every source clip. Keep speakers, songs and source stations disjoint between tuning and test splits.

The CC0 examples are [Eb major piano](https://commons.wikimedia.org/wiki/File:Eb_major_piano.ogg) and [Female vibrato](https://commons.wikimedia.org/wiki/File:Female-vibrato-1.wav). Speech, news, advertisements and presenter samples still need a rights-cleared source or owner recording. Do not label the same spoken clip as several different speech kinds; that would manufacture the answer. A sample may carry `speech_kind` metadata, but the current classifier does not predict that field.

Use the Go API's audio analyzer for consistent decoding and classification:

```sh
make dev
python3 scripts/evaluate-audio.py audio/dataset/manifest.jsonl
```

The evaluator reports a clip-level confusion matrix and per-label precision, recall and F1. Each clip is predicted by the modal output over its analysis windows. `speech_over_music` and `speech_over_singing` are separate ground-truth labels; the current YAMNet API cannot distinguish them. The news/advertisement/presenter rows expose that semantic gap instead of masking it.
