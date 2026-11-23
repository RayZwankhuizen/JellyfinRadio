from collections import Counter


def classification_report(expected, predicted, labels, predictions):
    matrix = {label: Counter() for label in labels}
    for truth, guess in zip(expected, predicted, strict=True):
        if truth not in matrix or guess not in predictions:
            raise ValueError(f"unknown classification label: {truth!r} -> {guess!r}")
        matrix[truth][guess] += 1

    per_class = {}
    for label in dict.fromkeys((*labels, *predictions)):
        true_positive = matrix[label][label] if label in labels and label in predictions else 0
        false_positive = sum(matrix[truth][label] for truth in labels if truth != label) if label in predictions else 0
        support = sum(matrix[label].values()) if label in matrix else 0
        false_negative = support - true_positive
        precision = true_positive / (true_positive + false_positive) if true_positive + false_positive else None
        recall = true_positive / support if support else None
        f1 = 2 * precision * recall / (precision + recall) if precision is not None and recall is not None and precision + recall else 0.0 if support else None
        per_class[label] = {"support": support, "precision": precision, "recall": recall, "f1": f1}

    total = len(expected)
    correct = sum(matrix[label][label] for label in labels if label in predictions)
    return {
        "count": total,
        "accuracy": correct / total if total else None,
        "labels": list(dict.fromkeys((*labels, *predictions))),
        "predictions": list(predictions),
        "confusion_matrix": {label: {guess: matrix[label][guess] for guess in predictions} for label in labels},
        "per_class": per_class,
    }
