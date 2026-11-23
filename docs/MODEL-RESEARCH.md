# Audio model review — Phase 2

Reviewed 2026-11-21. The first objective is to avoid reducing music or singing by mistake. A missed speech window is preferable to a false duck.

## Model comparison

| Candidate | Relevant capability | Runtime and window | Assessment for Smart Radio |
|---|---|---|---|
| Google YAMNet | 521 independent AudioSet event scores, including Speech, Singing and Music | 3.7M parameters; minimum 975 ms input | Small and already in the project, but it is a general event tagger, not a speech/music-mixture detector. Its scores are multi-label, not calibrated mutually exclusive probabilities. Existing mapping takes the maximum within hand-selected label groups and applies fixed thresholds. |
| PANNs | AudioSet event tagging; several CNN sizes, including Cnn6/Cnn10 | Cnn6 checkpoint 23.7 MB, Cnn10 25.2 MB; weak clip labels trained on 10-second examples | Good broad tagging and transfer-learning starting point. It does not add a dedicated speech-over-music class; larger variants raise CPU and packaging cost. Not a direct fix for the observed mixture failure. |
| inaSpeechSegmenter | Speech/music/noise segmentation; singing is intentionally tagged as music; speech-over-music is tagged speech | Older CNN/Keras stack; frame segmentation | Semantically close and MIT-licensed code. It merges speech-over-music with speech, so it cannot drive source-only ducking. Its documented singing behavior is useful as a conservative comparison. |
| inaVAMOS | Independent speech and music detectors, combined into speech, music, speech+music and other; 20 ms decisions | About 80 MB of models; Music2Vec/Transformer encoder; reports 30× real-time on an unspecified laptop CPU | Strongest documented radio-like speech/music segmentation candidate. The project states a research-only license for code and models, so it is excluded from this product until rights permit deployment. Its speed result is not an Athlon 3050U benchmark. |
| Silero VAD v5 | Speech activity probability on small PCM frames; not speech recognition and produces no transcript | Small streaming ONNX model; browser wrapper can run it in ONNX Runtime Web | Useful speech evidence, not a complete radio classifier: VAD alone has no music, singing, or speech+music classes. The supplied MyMusics design uses `@ricky0123/vad-web` 0.0.31 and ONNX Runtime Web 1.22.0 with audio taken from the active player. Keep it as one ensemble signal. |
| SMAD CRNN | Four classes: speech/noise, speech/music, singing/music, none | 834,728 parameters; fixed 4-second window; PyTorch model card, no published ONNX artifact | Most relevant small taxonomy. Its card reports 87.95% accuracy and 0.879 macro-F1 on a self-generated synthetic split. It explicitly says this is an upper bound and no genuine-recording evaluation exists. Training provenance includes CC BY-NC MUSDB18-HQ and a crawled subset with no license grant; do not ship these weights without a rights review and independent radio validation. |
| Speech/music specialist CNNs | Research literature reports radio speech/music segmentation with spectrogram CNNs | Runtime and model artifacts vary by paper | Better task fit than AudioSet taggers, but results cannot be transferred without acquiring the same data, code, license, and evaluation protocol. |
| ONNX Runtime | CPU inference runtime, not a classifier | Can run a suitably exported small network | Runtime format alone does not fix label or domain mismatch. YAMNet ONNX ports exist, but they retain YAMNet's 521-class task. No verified, permissively licensed ONNX artifact for the four-class SMAD model was found. |
| Demucs / music vocal separators | Splits musical vocals from accompaniment | Official CPU guidance is roughly 1.5× the audio duration | Not suitable for a continuous 1× stream on a two-core Athlon. Its vocal stem contains singing as well as speech; it cannot meet the distinction by itself. It may also introduce artifacts and buffering. |
| JRSV research system | Jointly separates speech and singing tracks, then runs recognition | Research model; paper describes a Chinese speech/singing benchmark | Interesting direction for future work, but not a small, verified, permissively licensed, ready-to-run ONNX component for this deployment. |

## Why the previous YAMNet test failed

The previous decision layer converted many independent AudioSet scores into three values by taking the maximum over broad, manually selected label sets (`speech`, `singing`, `music`), then compared them to fixed thresholds. That loses co-occurrence information and treats independent sigmoid-like event scores as if they were competing class probabilities. A `Music` score can dominate while a lower `Speech` score is hidden by the `0.55` cutoff. YAMNet has no explicit `speech+music` output class.

The model's minimum input is 975 ms and the evaluation used 50% overlapping windows. That temporal resolution is useful for coarse event monitoring, but a short speech/music transition can be blended within a window. The 9.5-second singing fixture is a sustained isolated vibrato sample, not a representative song; only 1 of 19 windows passed the selected singing rule and the rest were uncertain. The speech+piano fixture was classified as music in all 8 windows. Raw per-class scores were not retained in that prior report, so attributing the exact error to one score is an inference, not a measured fact.

YAMNet was trained for 521 AudioSet event tags using roughly one-second patches; it is not a specialized broadcast segmentation model. PANNs has a similar AudioSet tagging objective and longer training clips, so replacing one with the other would not create a dedicated mixture class.

## Category boundary

Acoustically separating speech, singing, music, and speech+music is a defined classifier task. Reliably distinguishing a newsreader, presenter, and advertisement is a different semantic task. An acoustic classifier cannot infer those categories consistently from voice timbre alone: it needs transcripts/context, station schedules, or explicit user tags. For this phase, the engine must expose separate gain settings for news, ads, and presenters but must not pretend those labels are automatically known. A future transcript classifier would require multilingual evaluation and additional latency/privacy review.

## Browser VAD path (MyMusics reference)

`@ricky0123/vad-web` wraps Silero ONNX for browser-side speech activity detection; ONNX Runtime Web provides inference. This is not speech-to-text. The MyMusics arrangement forks the active playback audio through Web Audio and supplies a `MediaStreamDestination` stream to the VAD. It does not need `getUserMedia` or an ASR service. The library's default `MicVAD` path requests a microphone, so an implementation must override its stream provider with the playback-derived stream; using the default would violate the no-microphone requirement. That web app owns the player element and can lower its own element volume on VAD events.

This client-side path can only adjust audio in the client that owns that Web Audio graph. It cannot control Jellyfin Web, a TV app, or Fire TV playback from this separate management page. Jellyfin's built-in M3U tuner has its own player pipeline. Server-side proxy processing is therefore still required for Jellyfin-wide behavior. The existing Go proxy forwards compressed upstream bytes and the current audio service accepts bounded clip uploads; there is no inline decoder/processor/output path. The VAD library/model is documented but not installed or wired in this checkout. A VAD event may be used as speech evidence, but not as the only demuting decision: songs and vocal-like material need an independent music/singing guard, and mixed speech/music remains untouched unless a source-aware model has passed the separation-quality tests.

## Acceptance targets

For an automatic full-stream gain change, require a source-disjoint, station-representative holdout with at least 1,000 windows per acoustic category and at least 10 stations:

| Safety measure | Minimum |
|---|---:|
| Speech-only precision | 0.995 |
| Speech-only recall | 0.95 |
| Speech-over-music precision / recall | 0.98 / 0.90 |
| Instrumental or singing windows incorrectly ducked | ≤0.5% each |
| Confidence calibration | Expected calibration error ≤0.05 |
| Audio processing | 95th-percentile CPU processing time below audio duration; bounded queue ≤1 s |

The false-duck ceiling is intentionally stricter than the speech recall target: music and singing must remain at normal level. To claim speech-component ducking, also require the isolated music accompaniment to remain within ±1 dB and the speech component to reach the selected gain within ±1 dB on independent stems. No current candidate has demonstrated those figures on this project's radio holdout.

## Decision

Do not make YAMNet, inaVAMOS, or SMAD the production speech-detection authority. YAMNet remains an analysis baseline only. inaVAMOS is research-only licensed; SMAD has a relevant taxonomy but unresolved training-data and domain-validation risks. Do not activate automatic ducking or claim source-only separation until a rights-cleared model and real radio holdout meet the targets above. A per-station setting cannot compensate for an unsafe classifier.

## Sources

- [YAMNet upstream model README](https://github.com/tensorflow/models/blob/master/research/audioset/yamnet/README.md)
- [PANNs paper](https://arxiv.org/abs/1912.10211) and [pretrained model archive](https://zenodo.org/records/3960586)
- [inaSpeechSegmenter](https://github.com/ina-foss/inaSpeechSegmenter)
- [inaVAMOS documentation, evaluation and license](https://github.com/ina-foss/inaVAMOS/)
- [Silero VAD v5 model history](https://github.com/snakers4/silero-vad/wiki/Version-history-and-Available-Models)
- [Browser VAD user guide and custom-stream support](https://github.com/ricky0123/vad/blob/master/docs/user-guide/browser.md)
- [ONNX Runtime Web documentation](https://onnxruntime.ai/docs/tutorials/web/)
- [SMAD model card, benchmark and training-data caveats](https://huggingface.co/duclvQ/smad)
- [Speech/music discrimination research](https://www.iit.demokritos.gr/sites/default/files/speech.pdf)
- [Demucs CPU and source documentation](https://github.com/facebookresearch/demucs)
- [JRSV speech/singing separation paper](https://arxiv.org/abs/2404.11275)
