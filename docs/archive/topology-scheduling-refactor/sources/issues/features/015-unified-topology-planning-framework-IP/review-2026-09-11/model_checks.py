"""Small, independent mathematical models; NOT Volcano integration tests.

Run: python3 model_checks.py
Checks safe filtering, a factor-wise optimistic bound, exact search against
exhaustive enumeration, and counterexamples relevant to the design review.
Only standard-library modules are used. Fixed seed makes results reproducible.
"""
import itertools
import json
import random
from dataclasses import dataclass


@dataclass
class Problem:
    allowed: list
    capacity: list
    unary: list
    hard: list  # (task i, task j, same-domain bool); domain(node) = node // 2
    soft: list  # (task i, task j, same-domain reward, different-domain reward)

    def valid(self, placement):
        if any(n not in self.allowed[t] for t, n in enumerate(placement)):
            return False
        if any(placement.count(n) > cap for n, cap in enumerate(self.capacity)):
            return False
        return all(((placement[i] // 2 == placement[j] // 2) == same)
                   for i, j, same in self.hard if j < len(placement))

    def closed_score(self, placement):
        score = sum(self.unary[t][n] for t, n in enumerate(placement))
        for i, j, same, different in self.soft:
            if j < len(placement):
                score += same if placement[i] // 2 == placement[j] // 2 else different
        return score

    def upper(self, placement):
        # Relax capacity and unassigned hard constraints. Every completion
        # satisfies this bound; it need not itself be a feasible completion.
        score = self.closed_score(placement)
        for t in range(len(placement), len(self.allowed)):
            if not self.allowed[t]:
                return float('-inf')
            score += max(self.unary[t][n] for n in self.allowed[t])
        score += sum(max(same, different) for i, j, same, different in self.soft
                     if j >= len(placement))
        return score


def exhaustive(p):
    return [(p.closed_score(x), x)
            for x in itertools.product(*p.allowed) if p.valid(x)]


def solve(p, limit=None):
    incumbent = None
    examined = 0
    exhausted = False
    pruned = 0

    def visit(partial):
        nonlocal incumbent, examined, exhausted, pruned
        if incumbent is not None and p.upper(partial) <= incumbent[0]:
            pruned += 1
            return
        if len(partial) == len(p.allowed):
            candidate = (p.closed_score(partial), partial)
            if incumbent is None or candidate[0] > incumbent[0]:
                incumbent = candidate
            return
        t = len(partial)
        for n in sorted(p.allowed[t], key=lambda n: (-p.unary[t][n], n)):
            # Charge failed checks too, not only successful allocations.
            if limit is not None and examined >= limit:
                exhausted = True
                return
            examined += 1
            child = partial + (n,)
            if p.valid(child):
                visit(child)
            if exhausted:
                return

    visit(())
    # A root bound is deliberately conservative when interrupted: it covers
    # ALL omitted branches. A production frontier could provide a tighter one.
    upper = p.upper(()) if exhausted else (incumbent[0] if incumbent else None)
    return incumbent, upper, examined, exhausted, pruned


def joint_repair(p, initial, limit):
    """One bounded neighborhood around a fixed complete incumbent.

    Freeze all but one/two tasks. Charge every proposed layout, even invalid
    ones. This enumerates a small neighborhood, not a globally optimal solver.
    """
    best = (p.closed_score(initial), initial)
    examined = 0
    for size in (1, 2):
        for window in itertools.combinations(range(len(initial)), size):
            for values in itertools.product(*(p.allowed[t] for t in window)):
                if examined >= limit:
                    return best, examined
                examined += 1
                candidate = list(initial)
                for t, node in zip(window, values):
                    candidate[t] = node
                candidate = tuple(candidate)
                if p.valid(candidate) and p.closed_score(candidate) > best[0]:
                    best = (p.closed_score(candidate), candidate)
    return best, examined


def check_models():
    rng = random.Random(15023911)
    instances = 500
    prefix_checks = 0
    bounded_runs = 0
    pruned = 0
    repair_checks = 0
    for _ in range(instances):
        m, n = 4, 4
        allowed = [tuple(v for v in range(n) if rng.random() < .75) for _ in range(m)]
        hard, soft = [], []
        for i, j in itertools.combinations(range(m), 2):
            if rng.random() < .22:
                hard.append((i, j, rng.choice([False, True])))
            if rng.random() < .6:
                soft.append((i, j, rng.randrange(-30, 61), rng.randrange(-30, 61)))
        p = Problem(allowed, [rng.randrange(1, 4) for _ in range(n)],
                    [[rng.randrange(101) for _ in range(n)] for _ in range(m)], hard, soft)
        oracle = exhaustive(p)
        optimum = max((v for v, _ in oracle), default=None)
        if oracle:
            # Use the first feasible assignment from a fixed enumeration;
            # do not initialize the local repair with the optimal solution.
            initial = oracle[0][1]
            previous = p.closed_score(initial)
            for repair_limit in (0, 4, 16, 64):
                repaired, cost = joint_repair(p, initial, repair_limit)
                assert cost <= repair_limit and p.valid(repaired[1])
                assert previous <= repaired[0] <= optimum
                previous = repaired[0]
                repair_checks += 1
        result, upper, _, exhausted, cuts = solve(p)
        pruned += cuts
        assert not exhausted
        assert (result[0] if result else None) == optimum
        assert upper == optimum
        # Every prefix of every feasible layout survives partial hard filters.
        for value, layout in oracle:
            for k in range(m + 1):
                prefix_checks += 1
                assert p.valid(layout[:k])
                assert p.upper(layout[:k]) >= value
        for limit in (0, 1, 3, 8, 32):
            bounded_runs += 1
            candidate, upper, cost, _, _ = solve(p, limit)
            assert cost <= limit
            if candidate:
                assert p.valid(candidate[1])
                assert candidate[0] <= optimum
                assert upper >= optimum
            elif optimum is not None:
                assert upper >= optimum

    # All-hard fit-first trap: task B can use only node 0.
    p = Problem([(0, 1), (0,)], [1, 1], [[100, 99], [100, 0]], [], [])
    result, _, _, _, _ = solve(p)
    assert result[1] == (1, 0)

    # First feasible need not maximize joint score even with static unary scores.
    p = Problem([(0, 1), (0, 1)], [1, 1], [[100, 99], [100, 0]], [], [])
    greedy = (0, 1)
    best = max(exhaustive(p))
    assert p.closed_score(greedy) == 100 and best[0] == 199
    repaired, repair_cost = joint_repair(p, greedy, 8)
    assert repaired[0] == 199 and repair_cost <= 8

    # Potential based on pair collisions counts distinct subjects, once/pair.
    counts = [2, 1, 1]
    phi = lambda c: -sum(v * (v - 1) // 2 for v in c)
    marginal = []
    for domain in range(3):
        after = counts[:]
        after[domain] += 1
        marginal.append(phi(after) - phi(counts))
    assert marginal == [-2, -1, -1]
    final_domains = [0, 1, 0, 2]
    totals = set()
    for order in itertools.permutations(range(4)):
        c, total = counts[:], 0
        for task in order:
            before = phi(c)
            c[final_domains[task]] += 1
            total += phi(c) - before
        totals.add(total)
    assert len(totals) == 1

    # Concrete failure of a (task-role,node) failure cache across rollback.
    free, cache = [0, 1], {}
    cache[('role-b', 0)] = free[0] < 1
    free[0] += 1  # undo A -> node 0
    assert cache[('role-b', 0)] and free[0] >= 1

    # NodeAffinity.Score sums term weights; normalizing changes competition.
    raw_na, topology = [200, 0], [0, 150]
    normalized_na = [100, 0]
    raw_winner = max(range(2), key=lambda i: raw_na[i] + topology[i])
    normalized_winner = max(range(2), key=lambda i: normalized_na[i] + topology[i])
    assert raw_winner == 0 and normalized_winner == 1

    # Candidate-relative normalization can reverse two unchanged nodes' rank.
    raw, other = {'A': 10, 'B': 20, 'C': 100}, {'A': 40, 'B': 0, 'C': 0}
    def score(cohort, node):
        return 100 * raw[node] / max(raw[n] for n in cohort) + other[node]
    assert score('AB', 'B') > score('AB', 'A')
    assert score('ABC', 'A') > score('ABC', 'B')

    # Summed free scalar resource is necessary but not sufficient for placement.
    assert sum([3, 3]) >= 4 and not any(free >= 4 for free in [3, 3])
    # One first-branch apply can consume all of B if the repaired suffix is long.
    repair_depth, extra_attempt_budget = 20, 8
    assert repair_depth > extra_attempt_budget

    return {
        'scope': 'independent finite mathematical model, not Volcano tests or performance evidence',
        'seed': 15023911, 'random_instances': instances,
        'exhaustive_optimum_matches': instances,
        'feasible_prefix_and_upper_bound_checks': prefix_checks,
        'bounded_search_runs': bounded_runs, 'safe_bound_prunes': pruned,
        'bounded_joint_repair_checks': repair_checks,
        'joint_repair_counterexample_score': repaired[0],
        'fit_first_solution': list(result[1]),
        'greedy_joint_score': 100, 'optimal_joint_score': 199,
        'peer_collision_marginals': marginal,
        'potential_order_permutations': 24,
        'stale_failure_cache_counterexample': 'confirmed in model',
        'raw_nodeaffinity_winner': 'A', 'normalized_nodeaffinity_winner': 'B',
        'candidate_cohort_relative_rank_flip': 'confirmed',
        'aggregate_capacity_not_sufficient': 'confirmed',
        'status': 'PASS',
    }


if __name__ == '__main__':
    print(json.dumps(check_models(), indent=2, ensure_ascii=False))
