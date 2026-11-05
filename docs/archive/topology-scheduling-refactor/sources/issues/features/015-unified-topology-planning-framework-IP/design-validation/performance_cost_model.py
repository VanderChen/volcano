"""Count a documented successful Volcano topology path; do not time it.

Source baseline: 7947e875600ff450318b412bb0c8c39f7020238a.
Assumptions: one successful gradient, no failures or elastic re-entry, every
candidate allocates the same working set. This is an independent operation
ledger, not a scheduler implementation or a plugin/latency benchmark.
"""

import heapq
import json
import random
from collections import Counter
from fractions import Fraction
from pathlib import Path


class Ledger:
    def __init__(self):
        self.calls = Counter()
        self.active = {}

    def apply(self, operations, choose=False):
        for task, node in operations:
            assert task not in self.active
            self.calls["allocate"] += 1
            self.calls["decide"] += int(choose)
            self.active[task] = node

    def save(self, operations):
        saved = []
        for task, node in operations:
            self.calls["task_clone"] += 1
            saved.append((task, node))
        return saved

    def undo(self, operations):
        for task, node in reversed(operations):
            assert self.active.pop(task) == node
            self.calls["undo"] += 1


def old_path(group_sizes, inner_counts):
    ledger = Ledger()
    outer_saved = []
    for j, counts in enumerate(inner_counts):
        job_operations = []
        for g, count in enumerate(counts):
            inner_saved = []
            for candidate in range(count):
                operations = [((g, t), (j, g, candidate))
                              for t in range(group_sizes[g])]
                ledger.apply(operations, choose=True)
                inner_saved.append(ledger.save(operations))
                ledger.undo(operations)
            chosen = inner_saved[0]
            ledger.apply(chosen)  # RecoverOperations for SubJob.
            job_operations.extend(chosen)
        outer_saved.append(ledger.save(job_operations))
        ledger.undo(job_operations)
        assert not ledger.active
    ledger.apply(outer_saved[0])  # RecoverOperations for Job.
    assert len(ledger.active) == sum(group_sizes)
    return ledger


def new_first_path(group_sizes):
    ledger = Ledger()
    operations = [((g, t), (0, g, 0))
                  for g, size in enumerate(group_sizes) for t in range(size)]
    ledger.apply(operations, choose=True)
    # Ownership is handed off with the selected state still active.
    assert len(ledger.active) == sum(group_sizes)
    return ledger


def verify_counts(group_sizes, inner_counts):
    m = sum(group_sizes)
    j_count = len(inner_counts)
    w = sum(count * size for row in inner_counts
            for count, size in zip(row, group_sizes))
    old = old_path(group_sizes, inner_counts)
    new = new_first_path(group_sizes)
    expected = dict(decide=w, allocate=w + j_count*m + m,
                    undo=w + j_count*m, task_clone=w + j_count*m)
    for key, count in expected.items():
        assert old.calls[key] == count, (key, old.calls, expected)
    for key, count in dict(decide=m, allocate=m, undo=0, task_clone=0).items():
        assert new.calls[key] == count
    assert old.active == new.active
    return {"M": m, "outer_candidates": j_count, "W": w,
            "old": expected,
            "new_first": {key: new.calls[key] for key in expected}}


def lazy_order(scores, override=None):
    """Freeze scalar scores and tie keys; select once, heap only for the rest."""
    first = override if override is not None else min(
        range(len(scores)), key=lambda node: (-scores[node], node))
    yield first
    heap = [(-score, node) for node, score in enumerate(scores) if node != first]
    heapq.heapify(heap)
    while heap:
        yield heapq.heappop(heap)[1]


def ownership_checks():
    """Check transfer vs undo on an abstract stack, not actual plugin safety."""
    tested = 0
    for depth in range(1, 17):
        operations = [(task, task % 3) for task in range(depth)]
        for stop_depth in range(depth + 1):
            for version_valid in (False, True):
                for same_winner in (False, True):
                    ledger = Ledger()
                    marks = []
                    for operation in operations[:stop_depth]:
                        marks.append([operation])
                        ledger.apply([operation])
                    expected = dict(operations)
                    deliver = (stop_depth == depth and version_valid and same_winner)
                    # Propagate a transfer token through all active frames.
                    for frame in reversed(marks):
                        if not deliver:
                            ledger.undo(frame)
                    if deliver:
                        assert ledger.active == expected
                        assert ledger.calls["undo"] == 0
                    else:
                        assert not ledger.active
                        assert ledger.calls["undo"] == stop_depth
                    tested += 1
    return tested


def main():
    rng = random.Random(20260911)
    scenarios = []
    for j_count, s in ((1, 1), (1, 4), (4, 1), (4, 4)):
        result = verify_counts([4, 6], [[s, s] for _ in range(j_count)])
        result["inner_candidates_each"] = s
        scenarios.append(result)
    for _ in range(400):
        sizes = [rng.randint(1, 8) for _ in range(rng.randint(1, 6))]
        counts = [[rng.randint(1, 5) for _ in sizes]
                  for _ in range(rng.randint(1, 5))]
        verify_counts(sizes, counts)

    # Independently evaluate the difference identity with exact rational costs.
    for _ in range(400):
        m, j_count, s = (rng.randint(1, 12) for _ in range(3))
        w = m * j_count * s
        costs = [Fraction(rng.randint(1, 100), rng.randint(1, 9))
                 for _ in range(8)]
        h_old, h_new, f_old, f_new, a, u, c, inc = costs
        old = h_old + w*f_old + (w+j_count*m+m)*a + (w+j_count*m)*(u+c)
        new = h_new + m*f_new + m*a + m*inc
        diff = h_new-h_old + m*f_new-w*f_old + m*inc - (w+j_count*m)*(a+u+c)
        assert new-old == diff

    for _ in range(500):
        scores = [rng.randrange(-10, 11) for _ in range(rng.randint(1, 80))]
        override = rng.randrange(len(scores)) if rng.randrange(2) else None
        expected = sorted(range(len(scores)), key=lambda n: (-scores[n], n))
        if override is not None:
            expected.remove(override)
            expected.insert(0, override)
        assert list(lazy_order(scores, override)) == expected

    quality_examples = []
    rho = Fraction(1, 10)
    for base in (Fraction(4, 5), Fraction(1), Fraction(21, 20), Fraction(6, 5)):
        upper = base * (1+rho)
        quality_examples.append({"assumed_first_ratio": float(base),
                                 "assumed_quality_fraction": float(rho),
                                 "conditional_total_ratio_upper": float(upper),
                                 "conditional_change_percent_upper": float(100*(upper-1))})
    assert [entry["conditional_change_percent_upper"] for entry in quality_examples] == [
        -12.0, 10.0, 15.5, 32.0]
    result = {
        "status": "PASS",
        "kind": "independent operation-count model; NO timings or Volcano execution",
        "source_baseline": "7947e875600ff450318b412bb0c8c39f7020238a",
        "assumptions": ["one successful gradient, no previous failed attempts",
                        "every candidate completes the same fixed working set",
                        "no elastic SubGroup re-entry; Allocate-only path",
                        "new first branch succeeds and hands off its active statement"],
        "document_scenarios": scenarios,
        "random_operation_ledger_cases": 400,
        "exact_cost_identity_cases": 400,
        "lazy_order_equivalence_cases": 500,
        "abstract_ownership_cases": ownership_checks(),
        "conditional_quality_examples_NOT_measurements": quality_examples,
    }
    output = Path(__file__).with_name("performance-cost-results.json")
    output.write_text(json.dumps(result, indent=2, ensure_ascii=False) + "\n")
    print(json.dumps(result, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
