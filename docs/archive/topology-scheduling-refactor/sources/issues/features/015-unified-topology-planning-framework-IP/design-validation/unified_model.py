"""Independent finite model of one bounded discrepancy/branch-and-bound planner.

This is design evidence, not Volcano implementation, plugin simulation or a
production benchmark. All fixtures use the same search and factor interfaces.
Run: python3 unified_model.py
"""
import itertools
import json
import math
import random
from collections import Counter, defaultdict
from dataclasses import dataclass
from pathlib import Path


class BudgetEnd(Exception):
    pass


class Meter:
    def __init__(self, limit=None):
        self.limit, self.used = limit, 0
        self.kinds = Counter()

    def charge(self, kind, amount=1):
        if self.limit is not None and self.used + amount > self.limit:
            raise BudgetEnd
        self.used += amount
        self.kinds[kind] += amount


@dataclass
class Problem:
    capacity: tuple
    demand: tuple
    allowed: tuple
    unary: tuple
    groups: tuple = ()  # (task IDs, collision penalty per unordered pair)
    hard: tuple = ()  # (task1, task2, node->domain tuple, same bool)
    compact: tuple = ()  # (task IDs, node->domain tuple, penalty if split)
    binpack: float = 0

    @property
    def m(self):
        return len(self.demand)

    @property
    def n(self):
        return len(self.capacity)

    @property
    def r(self):
        return len(self.capacity[0])

    def loads(self, x):
        a = [[0] * self.r for _ in range(self.n)]
        for t, n in enumerate(x):
            if n >= 0:
                for r in range(self.r):
                    a[n][r] += self.demand[t][r]
        return a

    def valid(self, x):
        if any(n >= 0 and n not in self.allowed[t] for t, n in enumerate(x)):
            return False
        if any(u > c for us, cs in zip(self.loads(x), self.capacity)
               for u, c in zip(us, cs)):
            return False
        return all(x[a] < 0 or x[b] < 0 or ((domain[x[a]] == domain[x[b]]) == same)
                   for a, b, domain, same in self.hard)

    def collision_cost(self, x):
        total = 0
        for tasks, weight in self.groups:
            counts = Counter(x[t] for t in tasks if x[t] >= 0)
            total += weight * sum(k * (k - 1) / 2 for k in counts.values())
        return total

    def cost(self, x):
        value = -sum(self.unary[t][n] for t, n in enumerate(x) if n >= 0)
        value += self.collision_cost(x)
        value += sum(weight for tasks, domain, weight in self.compact
                     if len({domain[x[t]] for t in tasks if x[t] >= 0}) > 1)
        if self.binpack:
            loads = self.loads(x)
            reward = 0
            for t, n in enumerate(x):
                if n >= 0:
                    dimensions = [r for r in range(self.r) if self.demand[t][r] > 0]
                    reward += sum(loads[n][r] / self.capacity[n][r]
                                  for r in dimensions) / len(dimensions)
            value -= self.binpack * 100 * reward / self.m
        return value

    def domains(self, x, meter=None):
        result = {}
        for t, node in enumerate(x):
            if node >= 0:
                continue
            available = []
            for n in self.allowed[t]:
                if meter:
                    meter.charge('support_check', 1 + self.m * self.r + len(self.hard))
                x[t] = n
                ok = self.valid(x)
                x[t] = -1
                if ok:
                    available.append(n)
            if not available:
                return None
            result[t] = available
        return result

    def propagate(self, x, domains, meter=None):
        if domains is None:
            return False
        classes = defaultdict(list)
        for t in domains:
            classes[self.demand[t]].append(t)
        used = self.loads(x)
        for q, tasks in classes.items():
            union = set().union(*(set(domains[t]) for t in tasks))
            if meter:
                meter.charge('slot_propagation', len(tasks) * self.n + len(union) * self.r)
            slots = sum(min((self.capacity[n][r] - used[n][r]) // q[r]
                            for r in range(self.r) if q[r] > 0) for n in union)
            if slots < len(tasks):
                return False
        return True

    def lower(self, x, domains, meter=None):
        if domains is None:
            return math.inf
        if meter:
            meter.charge('factor_bound', self.m * self.n +
                         sum(len(ts) * self.n for ts, _ in self.groups) +
                         sum(len(ts) for ts, _, _ in self.compact))
        value = -sum(self.unary[t][n] if n >= 0 else
                     max(self.unary[t][v] for v in domains[t])
                     for t, n in enumerate(x))
        for tasks, weight in self.groups:
            counts = [sum(x[t] == n for t in tasks) for n in range(self.n)]
            # Relax all unassigned eligibility/capacity; discrete convex water filling.
            for _ in range(sum(x[t] < 0 for t in tasks)):
                n = min(range(self.n), key=counts.__getitem__)
                counts[n] += 1
            value += weight * sum(k * (k - 1) / 2 for k in counts)
        value += sum(weight for tasks, domain, weight in self.compact
                     if len({domain[x[t]] for t in tasks if x[t] >= 0}) > 1)
        # Every resource utilization in a valid final plan is at most one.
        value -= 100 * self.binpack
        return value

    def order_score(self, x, t, n, heuristic='native'):
        score = self.unary[t][n]
        for tasks, weight in self.groups:
            if t not in tasks:
                continue
            counts = [sum(x[a] == v for a in tasks) for v in range(self.n)]
            # Count-aware local score; a deliberately poor ablation is also tested.
            if heuristic == 'blind':
                score -= weight * bool(counts[n])
            else:
                score -= weight * counts[n]
        used = self.loads(x)
        dimensions = [r for r in range(self.r) if self.demand[t][r] > 0]
        score += self.binpack * 100 / self.m * sum(
            (used[n][r] + self.demand[t][r]) / self.capacity[n][r]
            for r in dimensions) / len(dimensions)
        x[t] = n
        compact_delta = sum(weight for tasks, domain, weight in self.compact
                            if t in tasks and len({domain[x[a]] for a in tasks
                                                   if x[a] >= 0}) > 1)
        x[t] = -1
        # The compact term is only an ordering hint here; terminal cost is authoritative.
        return score - compact_delta

    def joined_root_lower(self, meter, max_cells=200000):
        """Join disjoint homogeneous count factors with capacity, ignoring labels.

        A generic, bounded aggregate relaxation: no role names or case IDs.
        Eligibility, pair constraints and compactness are relaxed. If factors
        cannot be represented this way or the table is too large, decline.
        """
        groups = [tuple(ts) for ts, _ in self.groups]
        flattened = [t for ts in groups for t in ts]
        if sorted(flattened) != list(range(self.m)):
            return None
        if any(len({self.demand[t] for t in ts}) != 1 for ts in groups):
            return None
        totals = tuple(len(ts) for ts in groups)
        q = [self.demand[ts[0]] for ts in groups]
        shape = math.prod(v + 1 for v in totals)
        estimate = self.n * shape * shape * (len(groups) * self.r + 1)
        if estimate > max_cells:
            return None
        meter.charge('joined_count_bound', estimate)
        patterns = []
        for capacity in self.capacity:
            choices = []
            for ys in itertools.product(*(range(v + 1) for v in totals)):
                if all(sum(ys[g] * q[g][r] for g in range(len(groups))) <= capacity[r]
                       for r in range(self.r)):
                    cost = sum(self.groups[g][1] * y * (y - 1) / 2
                               for g, y in enumerate(ys))
                    choices.append((ys, cost))
            patterns.append(choices)
        dp = {(0,) * len(groups): 0}
        for choices in patterns:
            following = {}
            for filled, value in dp.items():
                for ys, local in choices:
                    new = tuple(a + b for a, b in zip(filled, ys))
                    if all(a <= b for a, b in zip(new, totals)):
                        following[new] = min(following.get(new, math.inf), value + local)
            dp = following
        return dp.get(totals, math.inf) - sum(max(row[n] for n in self.allowed[t])
               for t, row in enumerate(self.unary)) - 100 * self.binpack


def solve(problem, order=None, budget=None, max_discrepancy=None, first_only=False,
          heuristic='native', use_bounds=True, use_propagation=True, joined_bound=False):
    p = problem
    order = tuple(range(p.m)) if order is None else tuple(order)
    assert sorted(order) == list(range(p.m))
    meter = Meter(budget)
    x = [-1] * p.m
    best, best_x = math.inf, None
    history, stats = [], Counter()
    stopped, stop_first = False, False
    limit = p.m if max_discrepancy is None else max_discrepancy
    contour = -1
    root_joined, joined_tried, certified = -math.inf, False, False

    def visit(depth, remaining):
        nonlocal best, best_x, stop_first, root_joined, joined_tried, certified
        meter.charge('visit')
        if remaining < 0 or remaining > p.m - depth:
            return
        domains = p.domains(x, meter)
        if domains is None or (use_propagation and not p.propagate(x, domains, meter)):
            stats['hard_prunes'] += 1
            return
        if use_bounds and max(p.lower(x, domains, meter), root_joined) >= best - 1e-9:
            stats['bound_prunes'] += 1
            return
        if depth == p.m:
            assert remaining == 0 and p.valid(x)
            meter.charge('terminal_evaluation', p.m * (p.r + p.n + 1))
            stats['leaves'] += 1
            value = p.cost(x)
            if value < best - 1e-9:
                best, best_x = value, tuple(x)
                history.append({'cost': best, 'work': meter.used, 'contour': contour})
            if first_only:
                stop_first = True
            elif joined_bound and not joined_tried:
                joined_tried = True
                bound = p.joined_root_lower(meter)
                if bound is not None:
                    root_joined = bound
                    certified = root_joined >= best - 1e-9
                    if certified:
                        stats['joined_root_certificate'] += 1
            return
        t = order[depth]
        candidates = domains[t]
        c = len(candidates)
        meter.charge('score_sort', c * (p.m * (p.r + p.n + 1) +
                                     max(1, math.ceil(math.log2(c)))))
        candidates = sorted(candidates, key=lambda n: (-p.order_score(x, t, n, heuristic), n))
        # Exact contours; prefer an early alternative within a nonzero contour.
        ranks = list(range(1, c)) + [0] if remaining else [0]
        for rank in ranks:
            discrepancy = int(rank != 0)
            if remaining < discrepancy or remaining - discrepancy > p.m - depth - 1:
                continue
            meter.charge('apply_and_reserved_undo', 2)
            x[t] = candidates[rank]
            stats['applies'] += 1
            try:
                visit(depth + 1, remaining - discrepancy)
            finally:
                x[t] = -1
                stats['undos'] += 1
            if stop_first or certified:
                return

    try:
        for contour in range(limit + 1):
            visit(0, contour)
            if stop_first or certified:
                break
            # A root bound can certify the incumbent without another contour.
            if best_x is not None and use_bounds:
                domains = p.domains(x, meter)
                if p.lower(x, domains, meter) >= best - 1e-9:
                    stats['root_certificate'] += 1
                    break
    except BudgetEnd:
        stopped = True
    assert x == [-1] * p.m and stats['applies'] == stats['undos']
    return {'cost': None if best_x is None else best, 'placement': best_x,
            'history': history, 'work': meter.used, 'work_kinds': dict(meter.kinds),
            'budget_exhausted': stopped, 'last_contour': contour, 'stats': dict(stats)}


def oracle(p):
    values = [(p.cost(x), x) for x in itertools.product(*p.allowed) if p.valid(x)]
    return min(values) if values else (None, None)


def random_checks():
    rng = random.Random(15020911)
    checked_prefixes = bounded_runs = 0
    for case in range(200):
        m, n = 4, 4
        hard = tuple((a, b, (0, 0, 1, 1), rng.choice((True, False)))
                     for a, b in itertools.combinations(range(m), 2) if rng.random() < .12)
        p = Problem(tuple((rng.randint(1, 4), rng.randint(1, 4)) for _ in range(n)),
                    tuple((rng.randint(1, 2), rng.randint(1, 2)) for _ in range(m)),
                    tuple(tuple(j for j in range(n) if rng.random() < .8) for _ in range(m)),
                    tuple(tuple(rng.randint(-20, 60) for _ in range(n)) for _ in range(m)),
                    groups=(((0, 1, 2), 7), ((2, 3), 11)), hard=hard,
                    compact=(((0, 3), (0, 0, 1, 1), 9),), binpack=.3)
        exact, _ = oracle(p)
        for order in (tuple(range(m)), tuple(reversed(range(m))), (1, 3, 0, 2)):
            found = solve(p, order)
            assert (found['cost'] is None) == (exact is None)
            if exact is not None:
                assert abs(found['cost'] - exact) < 1e-8
        previous = math.inf
        for budget in (0, 50, 500, 2000, 10000):
            run = solve(p, budget=budget)
            assert run['work'] <= budget
            value = math.inf if run['cost'] is None else run['cost']
            assert value <= previous + 1e-8
            if run['placement'] is not None:
                assert p.valid(run['placement']) and value >= exact - 1e-8
            previous = value
            bounded_runs += 1
        # Exhaustive prefixes and completions establish safe support/slot/bound behavior.
        for depth in range(m + 1):
            for prefix in itertools.product(*p.allowed[:depth]):
                x = list(prefix) + [-1] * (m - depth)
                if not p.valid(x):
                    continue
                domains = p.domains(x)
                completions = [p.cost(prefix + tail) for tail in
                               itertools.product(*p.allowed[depth:]) if p.valid(prefix + tail)]
                if completions:
                    assert p.propagate(x, domains)
                    assert p.lower(x, domains) <= min(completions) + 1e-8
                checked_prefixes += 1
    return {'instances': 200, 'exact_order_runs': 600,
            'bounded_runs': bounded_runs, 'prefix_checks': checked_prefixes}


def fixtures():
    cases = {}
    cases['disconnected_scope'] = Problem(((2,),) * 3, ((1,),) * 3,
        ((1, 2),) * 3, ((1000, 1, 0),) * 3, groups=(((0, 1, 2), 3),))
    cases['hard_topology_and_soft_sources'] = Problem(((2,),) * 4, ((1,),) * 3,
        ((0, 1, 2, 3),) * 3, ((4, 0, 1, 0), (0, 5, 0, 1), (9, 0, 7, 2)),
        hard=((0, 1, (0, 0, 1, 1), True), (1, 2, (0, 0, 1, 1), False)),
        groups=(((0, 1, 2), 2),), binpack=.1)
    cases['fit_first_dead_end'] = Problem(((1,),) * 2, ((1,),) * 2,
        ((0, 1), (0,)), ((100, 90), (100, 0)))
    cases['feasible_but_poor_joint_score'] = Problem(((1,),) * 2, ((1,),) * 2,
        ((0, 1),) * 2, ((100, 90), (100, 0)))
    for name, weight in (('binpack_vs_spread_low', .2), ('binpack_vs_spread_high', 1)):
        cases[name] = Problem(((4,),) * 2, ((1,),) * 2, ((0, 1),) * 2,
                             ((0, 0),) * 2, groups=(((0, 1), 10),), binpack=weight)
    cases['ordinary_preference_overrides_spread'] = Problem(((4,),) * 2, ((1,),) * 2,
        ((0, 1),) * 2, ((60, 0),) * 2, groups=(((0, 1), 10),), binpack=.2)
    m = 6
    cases['one_early_choice_changes_many_tasks'] = Problem(((1,),) * m, ((1,),) * m,
        tuple((i, (i + 1) % m) for i in range(m)),
        tuple(tuple((10 if n == 0 else 9) if i == 0 else (2 if n == (i + 1) % m else 0)
                    for n in range(m)) for i in range(m)))
    results = {}
    for name, p in cases.items():
        exact, _ = oracle(p)
        run = solve(p)
        assert abs(run['cost'] - exact) < 1e-8
        results[name] = run
    p = cases['feasible_but_poor_joint_score']
    greedy = solve(p, first_only=True)
    assert greedy['cost'] == -100 and results['feasible_but_poor_joint_score']['cost'] == -190
    results['feasible_but_poor_joint_score']['first_only_cost'] = greedy['cost']
    assert len(set(results['binpack_vs_spread_low']['placement'])) == 2
    assert len(set(results['binpack_vs_spread_high']['placement'])) == 1
    assert results['ordinary_preference_overrides_spread']['placement'] == (0, 0)
    shift = results['one_early_choice_changes_many_tasks']
    assert shift['placement'] == (1, 2, 3, 4, 5, 0)
    assert shift['history'][0]['contour'] == 1
    return results


def joined_checks():
    rng = random.Random(20260911)
    checked = solved = 0
    for _ in range(120):
        split, rest = rng.randint(1, 3), rng.randint(1, 3)
        m, n = split + rest, 3
        q1, q2 = (rng.randint(1, 2), rng.randint(1, 2)), (rng.randint(1, 2), rng.randint(1, 2))
        allowed = tuple(tuple(sorted({rng.randrange(n)} | {j for j in range(n)
                                                           if rng.random() < .7})) for _ in range(m))
        p = Problem(tuple((rng.randint(2, 5), rng.randint(2, 5)) for _ in range(n)),
            (q1,) * split + (q2,) * rest, allowed,
            tuple(tuple(rng.randint(-3, 15) for _ in range(n)) for _ in range(m)),
            groups=((tuple(range(split)), 3), (tuple(range(split, m)), 7)),
            hard=((0, m-1, (0, 1, 1), rng.choice((True, False))),),
            compact=(((0, m-1), (0, 1, 1), 8),), binpack=.1)
        exact, _ = oracle(p)
        bound = p.joined_root_lower(Meter())
        assert bound is not None
        if exact is not None:
            assert bound <= exact + 1e-8
        result = solve(p, joined_bound=True)
        assert (result['cost'] is None) == (exact is None)
        if exact is not None:
            assert abs(result['cost'] - exact) < 1e-8
            solved += 1
        checked += 1
    return {'instances': checked, 'feasible_instances': solved,
            'bound_and_exact_search_match_oracle': True}


def bug020():
    p = Problem(((8,),) * 3, ((2,),) * 8 + ((1,),) * 8, ((0, 1, 2),) * 16,
                ((0, 0, 0),) * 16, groups=((tuple(range(8)), 100 / 56),
                (tuple(range(8, 16)), 100 / 56)), binpack=1)
    orders = {'D-first': tuple(range(16)),
              'P-first': tuple(range(8, 16)) + tuple(range(8)),
              'interleaved': tuple(v for pair in zip(range(8), range(8, 16)) for v in pair)}
    result = {}
    optimal_cost = 100 * 15 / 56 - 100
    for name, order in orders.items():
        run = solve(p, order, budget=300000)
        assert run['cost'] is not None and abs(run['cost'] - optimal_cost) < 1e-8
        x = run['placement']
        run['decode'] = [x[:8].count(n) for n in range(3)]
        run['prefill'] = [x[8:].count(n) for n in range(3)]
        result[name] = run
        certified_run = solve(p, order, budget=300000, joined_bound=True)
        assert abs(certified_run['cost'] - optimal_cost) < 1e-8
        assert certified_run['stats'].get('joined_root_certificate') == 1
        run['with_generic_joined_bound'] = {
            key: certified_run[key] for key in ('cost', 'work', 'budget_exhausted', 'stats')}
    # Exact integer resource coupling is verified separately by all count layouts.
    aggregate = [(sum(d * (d - 1) // 2 + (8 - 2 * d) * (7 - 2 * d) // 2
                      for d in ds), ds) for ds in itertools.product(range(5), repeat=3)
                 if sum(ds) == 8]
    assert len(aggregate) == 15 and min(v for v, _ in aggregate) == 15
    result['aggregate_oracle'] = {'layouts': len(aggregate), 'collision_optimum': 15,
                                'cost_optimum': optimal_cost}
    return result


def contour_counts():
    rows = []
    for m, b in ((4, 3), (6, 3), (8, 2)):
        visits = 0
        def visit(depth, remaining):
            nonlocal visits
            visits += 1
            if depth == m:
                assert remaining == 0
                return
            for rank in range(b):
                following = remaining - int(rank != 0)
                if 0 <= following <= m - depth - 1:
                    visit(depth + 1, following)
        for remaining in range(m + 1):
            visit(0, remaining)
        expanded_formula = sum((m - i + 1) * b ** i for i in range(m + 1))
        closed_formula = (b ** (m + 2) - (m + 2) * b + (m + 1)) // (b - 1) ** 2
        assert visits == expanded_formula == closed_formula
        for d in range(min(m, 3) + 1):
            observed = sum(sum(v != 0 for v in x) <= d
                           for x in itertools.product(range(b), repeat=m))
            expected = sum(math.comb(m, j) * (b - 1) ** j for j in range(d + 1))
            assert observed == expected
            rows.append({'depth': m, 'branching': b, 'discrepancy': d,
                         'leaves_up_to_contour': observed, 'full_prefix_visits': visits})
    return rows


if __name__ == '__main__':
    report = {'scope': 'independent finite model; not Volcano or latency measurements',
              'random_checks': random_checks(), 'fixtures': fixtures(),
              'joined_relaxation_checks': joined_checks(),
              'bug020': bug020(), 'contour_counts': contour_counts(), 'status': 'PASS'}
    path = Path(__file__).with_name('unified-results.json')
    path.write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps({'status': report['status'], 'random_checks': report['random_checks'],
                      'bug020': {k: {f: v[f] for f in ('cost', 'work', 'last_contour',
                                  'budget_exhausted', 'decode', 'prefill')} for k, v in
                                  report['bug020'].items() if k != 'aggregate_oracle'}}, indent=2))
