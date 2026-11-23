from dataclasses import dataclass
SPEECH_LABELS = {
    "Speech", "Child speech, kid speaking", "Conversation", "Narration, monologue",
    "Babbling", "Speech synthesizer", "Shout", "Bellow", "Whoop", "Yell",
    "Children shouting", "Screaming", "Whispering",
}
SINGING_LABELS = {
    "Singing", "Choir", "Yodeling", "Chant", "Mantra", "Child singing",
    "Synthetic singing", "Rapping", "Humming",
}
MUSIC_LABELS = {
    "Music", "Musical instrument", "Song", "Background music", "Theme music",
    "Soundtrack music", "Vocal music", "Opera", "A capella", "Pop music",
    "Hip hop music", "Rock music", "Jazz", "Classical music", "Country",
    "Reggae", "Blues", "Electronic music", "Dance music", "Folk music",
}
JINGLE_LABELS = {"Jingle (music)"}


@dataclass(frozen=True)
class Scores:
    speech: float = 0.0
    singing: float = 0.0
    music: float = 0.0
    jingle: float = 0.0


@dataclass(frozen=True)
class Decision:
    category: str
    confidence: float
    scores: Scores


def summarize_scores(labels: list[str], values: list[float]) -> Scores:
    scores = dict(zip(labels, values, strict=True))
    return Scores(
        speech=max((scores.get(label, 0.0) for label in SPEECH_LABELS), default=0.0),
        singing=max((scores.get(label, 0.0) for label in SINGING_LABELS), default=0.0),
        music=max((scores.get(label, 0.0) for label in MUSIC_LABELS), default=0.0),
        jingle=max((scores.get(label, 0.0) for label in JINGLE_LABELS), default=0.0),
    )


def classify(scores: Scores, threshold: float = 0.55, singing_floor: float = 0.20) -> Decision:
    if scores.jingle >= threshold:
        return Decision("jingle", scores.jingle, scores)
    # Prefer a false negative to ducking singing when the classes overlap.
    if scores.singing >= singing_floor and scores.singing >= scores.speech * 0.5:
        return Decision("singing", scores.singing, scores)
    if scores.speech >= threshold:
        category = "speech_over_music" if scores.music >= threshold else "speech"
        return Decision(category, scores.speech, scores)
    if scores.music >= threshold:
        return Decision("music", scores.music, scores)
    return Decision("uncertain", max(scores.speech, scores.singing, scores.music), scores)


class SpeechGate:
    def __init__(self, enter: float = 0.75, exit: float = 0.55, attack_windows: int = 2, release_windows: int = 2):
        if not 0 <= exit <= enter <= 1 or attack_windows < 1 or release_windows < 1:
            raise ValueError("invalid speech gate configuration")
        self.enter, self.exit = enter, exit
        self.attack_windows, self.release_windows = attack_windows, release_windows
        self.active = False
        self._attack = self._release = 0

    def update(self, scores: Scores, singing_floor: float = 0.20) -> bool:
        singing = scores.singing >= singing_floor and scores.singing >= scores.speech * 0.5
        if singing:
            self.active = False
            self._attack = self._release = 0
        elif self.active:
            if scores.speech >= self.exit:
                self._release = 0
            else:
                self._release += 1
                if self._release >= self.release_windows:
                    self.active = False
                    self._release = 0
        elif scores.speech >= self.enter:
            self._attack += 1
            if self._attack >= self.attack_windows:
                self.active = True
                self._attack = 0
        else:
            self._attack = 0
        return self.active


class GainRamp:
    def __init__(self, fade_seconds: float = 1.5, initial_gain: float = 1.0):
        if fade_seconds <= 0 or not 0 <= initial_gain <= 1:
            raise ValueError("invalid gain ramp configuration")
        self.fade_seconds = fade_seconds
        self.current = initial_gain
        self._start = initial_gain
        self._target = initial_gain
        self._elapsed = fade_seconds

    def update(self, target: float, elapsed_seconds: float) -> float:
        if not 0 <= target <= 1 or elapsed_seconds < 0:
            raise ValueError("gain and elapsed time must be non-negative and gain at most 1")
        if target != self._target:
            self._start, self._target, self._elapsed = self.current, target, 0.0
        self._elapsed = min(self.fade_seconds, self._elapsed + elapsed_seconds)
        fraction = self._elapsed / self.fade_seconds
        self.current = self._start + (self._target - self._start) * fraction
        return self.current


class Ducker:
    """Analysis-only by default; mixed audio is never ducked unless explicitly opted in."""

    def __init__(self, speech_volume: float = 0.2, fade_seconds: float = 1.5,
                 threshold: float = 0.55, analysis_only: bool = True,
                 enabled: bool = False, allow_mixed_stream_ducking: bool = False):
        if not 0 <= speech_volume <= 1:
            raise ValueError("speech volume must be in [0, 1]")
        self.speech_volume = speech_volume
        self.threshold = threshold
        self.analysis_only = analysis_only
        self.enabled = enabled
        self.allow_mixed_stream_ducking = allow_mixed_stream_ducking
        self.gate = SpeechGate()
        self.ramp = GainRamp(fade_seconds)

    def update(self, scores: Scores, elapsed_seconds: float) -> tuple[Decision, float]:
        decision = classify(scores, self.threshold)
        active = self.gate.update(scores)
        target = 1.0
        if self.enabled and not self.analysis_only and active:
            if decision.category == "speech" or (decision.category == "speech_over_music" and self.allow_mixed_stream_ducking):
                target = self.speech_volume
        return decision, self.ramp.update(target, elapsed_seconds)
