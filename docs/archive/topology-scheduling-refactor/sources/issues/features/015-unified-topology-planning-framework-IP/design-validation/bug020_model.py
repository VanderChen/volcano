"""Independent Bug020/complexity model; does not execute Volcano code.

python3 bug020_model.py
Units are GPU cards; D consumes 2, P consumes 1. Three nodes have 8 each.
"""
import itertools
import json
import math
from fractions import Fraction


def collisions(counts):
    return sum(v * (v - 1) // 2 for v in counts)


def count_layouts():
    layouts = []
    for d in itertools.product(range(5), repeat=3):
        if sum(d) != 8:
            continue
        p = tuple(8 - 2 * value for value in d)
        assert sum(p) == 8 and min(p) >= 0
        cost = collisions(d) + collisions(p)
        layouts.append((cost, d, p))
    return sorted(layouts)


def counts(placement):
    d = tuple(placement[:8].count(n) for n in range(3))
    p = tuple(placement[8:].count(n) for n in range(3))
    return d, p


def valid(placement):
    d, p = counts(placement)
    return all(2 * dd + pp <= 8 for dd, pp in zip(d, p))


def cost(placement):
    d, p = counts(placement)
    return collisions(d) + collisions(p)


def make_placement(d, p):
    return tuple(n for n, value in enumerate(d) for _ in range(value)) + tuple(
        n for n, value in enumerate(p) for _ in range(value))


def neighborhood(initial, maximum_changed_tasks):
    best = cost(initial)
    best_layout = initial
    trials = feasible = 0
    for size in range(1, maximum_changed_tasks + 1):
        for window in itertools.combinations(range(16), size):
            # Enumerate changes, not assignments that leave a chosen task intact.
            alternatives = [tuple(n for n in range(3) if n != initial[t]) for t in window]
            for nodes in itertools.product(*alternatives):
                trials += 1
                candidate = list(initial)
                for t, n in zip(window, nodes):
                    candidate[t] = n
                candidate = tuple(candidate)
                if valid(candidate):
                    feasible += 1
                    value = cost(candidate)
                    if value < best:
                        best, best_layout = value, candidate
    return {'best_cost': best, 'best_counts': counts(best_layout),
            'trials': trials, 'feasible': feasible}


def slot_propagation(used, remaining_d, remaining_p):
    free = [8 - value for value in used]
    return (sum(free) >= 2 * remaining_d + remaining_p
            and sum(value // 2 for value in free) >= remaining_d
            and sum(free) >= remaining_p)


def ordered_search(role_order, max_checks=100000):
    """Simplified GTA count order plus GPU binpack, with safe slot pruning.

    Every inspected Node consumes work. Undo always restores the count state.
    This is not a simulation of all Volcano plugins.
    """
    used, d, p = [0] * 3, [0] * 3, [0] * 3
    checks = prunes = backtracks = 0
    path = []
    stopped = False

    def visit(index):
        nonlocal checks, prunes, backtracks, stopped
        if index == len(role_order):
            return True
        role = role_order[index]
        demand = 2 if role == 'D' else 1
        peers = d if role == 'D' else p
        largest = max(peers)
        def score(n):
            gta = Fraction(1) - (Fraction(peers[n], largest) if largest else 0)
            bp = Fraction(used[n] + demand, 8)
            return gta + bp
        candidates = sorted(range(3), key=lambda n: (-score(n), n))
        for n in candidates:
            if checks >= max_checks:
                stopped = True
                return False
            checks += 1
            if used[n] + demand > 8:
                continue
            used[n] += demand
            peers[n] += 1
            path.append(n)
            remaining = role_order[index + 1:]
            if slot_propagation(used, remaining.count('D'), remaining.count('P')):
                if visit(index + 1):
                    return True
            else:
                prunes += 1
            path.pop()
            used[n] -= demand
            peers[n] -= 1
            backtracks += 1
            if stopped:
                return False
        return False

    found = visit(0)
    if not found:
        assert used == d == p == [0, 0, 0]
    return {'found': found, 'decode': d[:], 'prefill': p[:],
            'checks': checks, 'capacity_prunes': prunes, 'backtracks': backtracks,
            'budget_exhausted': stopped, 'resource_used': used[:]}


def tree_check(branching, depth, budget=None):
    """No feasible leaves; charge every edge, to verify full/bounded counts."""
    edges = leaves = 0
    def visit(level):
        nonlocal edges, leaves
        if level == depth:
            leaves += 1
            return
        for _ in range(branching):
            if budget is not None and edges >= budget:
                return
            edges += 1
            visit(level + 1)
    visit(0)
    return edges, leaves


def main():
    layouts = count_layouts()
    assert len(layouts) == 15 and layouts[0][0] == 15
    optimal = [(d, p) for c, d, p in layouts if c == 15]
    assert len(optimal) == 3
    categories = {}
    for c, d, p in layouts:
        categories.setdefault(tuple(sorted(d, reverse=True)), (c, tuple(sorted(p, reverse=True))))
    bad = make_placement((4, 2, 2), (0, 4, 4))
    historical = make_placement((4, 3, 1), (0, 2, 6))
    one, two, three = [neighborhood(bad, k) for k in (1, 2, 3)]
    assert one['best_cost'] == two['best_cost'] == 20
    assert three['best_cost'] == 15
    historical_three = neighborhood(historical, 3)
    assert historical_three['best_cost'] == 15
    # P-first evenly fills 3/3/2 cards; remaining D capacity is only 7 pods.
    assert sum((8 - p) // 2 for p in (3, 3, 2)) == 7
    # Local Binpack scores depend on order, even with the SAME final layout.
    path_12 = Fraction(1, 8) + Fraction(3, 8)
    path_21 = Fraction(2, 8) + Fraction(3, 8)
    assert path_12 != path_21
    # Proposed terminal mean-Binpack reads final load, once per task.
    terminal = Fraction(3, 8)
    # Stable GPA source utility: mean of two role terms, each normalized by C(8,2).
    gta_bad = 100 * (1 - Fraction(20, 56))
    gta_good = 100 * (1 - Fraction(15, 56))
    assert gta_good > gta_bad
    # A hypothetical competing NodeAffinity cost can legitimately change the winner.
    na_bad, na_good = Fraction(100), Fraction(325, 4)
    assert gta_bad + na_bad > gta_good + na_good
    assert 3 * gta_bad + na_bad < 3 * gta_good + na_good
    orders = {'D-first': 'D' * 8 + 'P' * 8,
              'P-first': 'P' * 8 + 'D' * 8,
              'interleaved': 'DP' * 8}
    order_results = {name: ordered_search(order) for name, order in orders.items()}
    assert all(v['found'] and v['resource_used'] == [8, 8, 8] for v in order_results.values())
    budgeted = ordered_search(orders['P-first'], 3)
    assert not budgeted['found'] and budgeted['budget_exhausted']
    assert budgeted['checks'] == 3 and budgeted['resource_used'] == [0, 0, 0]
    complexity = []
    for depth in (2, 4, 6, 8):
        expected = sum(3 ** k for k in range(1, depth + 1))
        edges, leaves = tree_check(3, depth)
        bounded, _ = tree_check(3, depth, 64)
        assert edges == expected and leaves == 3 ** depth
        assert bounded == min(64, expected)
        complexity.append({'depth': depth, 'full_edges': edges,
                           'leaves': leaves, 'budget_64_edges': bounded})
    return {
        'scope': 'independent GPU/count model; not Volcano tests, not timing benchmarks',
        'aggregate_feasible_layouts': len(layouts), 'optimal_layouts': optimal,
        'layout_categories': [{'decode': d, 'prefill': p, 'collision_cost': c}
                              for d, (c, p) in categories.items()],
        'bad_layout_neighborhoods': {'one_task': one, 'two_tasks': two, 'three_tasks': three},
        'historical_layout_three_task_repair': historical_three,
        'prefill_first_even_layout_remaining_decode_slots': 7,
        'binpack_path_1_then_2': str(path_12), 'binpack_path_2_then_1': str(path_21),
        'binpack_terminal_same_final_layout': str(terminal),
        'gta_terminal_bad': float(gta_bad), 'gta_terminal_ideal': float(gta_good),
        'nodeaffinity_weight_override': 'weight 1 favors bad; GTA weight 3 favors ideal',
        'ordered_search_cases': order_results, 'budget_exhaustion': budgeted,
        'search_tree_counts': complexity, 'status': 'PASS',
    }


if __name__ == '__main__':
    print(json.dumps(main(), indent=2, ensure_ascii=False))
