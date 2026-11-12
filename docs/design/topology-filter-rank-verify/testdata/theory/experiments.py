"""Brute-force checks of the theorems in THEORY.md on the abstract model (model.py).

Run:  uv run --no-project python experiments.py [x1 x2 x3 x5 ...]
Every assertion that fails raises. The output is the evidence cited in THEORY.md.
"""
from __future__ import annotations

import itertools
import math
import random
import sys
import time

from model import (Job, Policy, SubJob, containers, gen_topology, hall, hall_tier, mtf, mtf_batch, oracle,
                   ranked, region, search, try_job)

POLICIES = [("bestfit", "bestfit"), ("emptiest", "emptiest"), ("name", "bestfit"), ("random", "random")]


def policy(rank, node, seed=0):
    return Policy(rank=rank, node=node, rng=random.Random(seed))


def fill(rng, topo):
    for n, cap in enumerate(topo.cap):
        topo.free[n] = rng.randint(0, cap) if rng.random() < 0.5 else cap


def add_exclusion(rng, topo, job):
    if topo.top < 3 or rng.random() < 0.5:
        return
    doms = [d for d in topo.domains if d.tier == 2]
    if len(doms) < 2:
        return
    job.cmp_tier = 2
    job.excluded = frozenset(d.name for d in rng.sample(doms, rng.randint(1, len(doms) - 1)))


def gen_homogeneous(rng, mixed_limits=True):
    """Class H: one task shape, one SubJob size, arbitrary tier limits."""
    topo = gen_topology(rng, tiers=rng.choice((2, 3, 3)), fan=(1, 3), per_rack=(1, 2), caps=(1, 2, 3, 4))
    fill(rng, topo)
    demand, size = rng.choice((1, 1, 2)), rng.choice((1, 1, 2, 3))
    m = rng.randint(1, 5)
    if mixed_limits:
        limits = [rng.choice((1, 1, 2, 3, None)) for _ in range(m)]
    else:
        limits = [rng.choice((1, 2, None))] * m
    job = Job([SubJob(f"u{i}", (demand,) * size, limits[i]) for i in range(m)], limit=rng.choice((None, None, 2, 3)))
    add_exclusion(rng, topo, job)
    return topo, job


def limit_key(topo, sub):
    return topo.top + 1 if sub.limit is None else sub.limit


def sorted_order(topo, job):
    return sorted(range(len(job.subjobs)), key=lambda i: (limit_key(topo, job.subjobs[i]), i))


def search_mtf(topo, job, order, pol):
    """Normal scan, but each container is retried with unbounded move-to-front."""
    reg = region(topo, job)
    conts = containers(topo, job, "region")
    node_sets = {d.name: d.nodes & reg for d in conts}
    demand = sum(sum(s.tasks) for s in job.subjobs)
    for d in ranked(topo, topo.free, conts, node_sets, demand, pol):
        placement, retries, _ = mtf(topo, job, d, order, pol)
        if placement is not None:
            return d.tier, retries
    return None, 0


def classes(topo, job, container):
    eff = [container.tier if s.limit is None else min(s.limit, container.tier) for s in job.subjobs]
    return [sum(1 for e in eff if e == h) for h in sorted(set(eff))]


def mtf_bound(counts):
    """Theorem 5: prod over all classes but the widest of (1 + m_c), minus 1."""
    return math.prod(1 + c for c in counts[:-1]) - 1


def x1(n=6000, seed=1):
    """Theorems 2, 3, 4: exactness on class H, and what the container reading loses."""
    rng = random.Random(seed)
    feasible = hall_bad = 0
    sorted_bad = {p: 0 for p in POLICIES}
    cont_miss = cont_wider = cont_lost = 0
    unsorted_miss = unsorted_wider = 0
    design_miss = design_wider = design_retries = 0
    mtf_bad = mtf_max = 0
    for _ in range(n):
        topo, job = gen_homogeneous(rng)
        want = oracle(topo, job)
        feasible += want is not None
        hall_bad += hall_tier(topo, job) != want
        for rank, node in POLICIES:
            _, tier, _ = search(topo, job, sorted_order(topo, job), policy(rank, node, seed))
            sorted_bad[(rank, node)] += tier != want
        pol = policy("bestfit", "bestfit")
        # Current reading of the exclusion constraint, same order.
        _, tier0, _ = search(topo, job, sorted_order(topo, job), pol, semantics="container")
        if want is not None and tier0 is None:
            cont_miss += 1
        if tier0 is not None and tier0 != want:
            cont_wider += 1
        # Theorem 2: whatever the current reading schedules, the region reading schedules identically.
        order = list(range(len(job.subjobs)))
        rng.shuffle(order)
        p0, t0, _ = search(topo, job, order, pol, semantics="container")
        p1, t1, _ = search(topo, job, order, pol, semantics="region", budget=4)
        if p0 is not None and (p1 != p0 or t1 != t0):
            cont_lost += 1
        # Arbitrary order: normal search, design retry (budget 4), ideal move-to-front.
        _, tier, _ = search(topo, job, order, pol)
        unsorted_miss += want is not None and tier is None
        unsorted_wider += tier is not None and tier != want
        _, tier, used = search(topo, job, order, pol, budget=4)
        design_miss += want is not None and tier is None
        design_wider += tier is not None and tier != want
        design_retries += used
        tier, retries = search_mtf(topo, job, order, pol)
        mtf_bad += tier != want
        mtf_max = max(mtf_max, retries)
    print(f"x1: {n} instances of class H, {feasible} feasible (oracle)")
    print(f"  counting condition (Thm 4a) disagrees with the oracle: {hall_bad}")
    for p in POLICIES:
        print(f"  narrowest-limit-first search, rank={p[0]} node={p[1]}: wrong answer or wrong tier: {sorted_bad[p]}")
    print(f"  container reading (current), same order: feasible but not scheduled {cont_miss}, scheduled at a wider tier {cont_wider}")
    print(f"  Thm 2: scheduled under the container reading but different under region + retry: {cont_lost}")
    print(f"  arbitrary order, no retry: missed {unsorted_miss}, wider tier {unsorted_wider}")
    print(f"  arbitrary order, design retry budget 4: missed {design_miss}, wider tier {design_wider}, retries used {design_retries}")
    print(f"  arbitrary order, unbounded move-to-front per container: wrong answer or tier {mtf_bad}, most retries in the winning container {mtf_max}")
    assert hall_bad == 0 and not any(sorted_bad.values()) and cont_lost == 0 and mtf_bad == 0


def x2(n=1500, seed=2):
    """Theorem 5: move-to-front never cycles on a placeable container of class H; retry bound."""
    rng = random.Random(seed)
    checked = runs = failures = over = 0
    batch_failures = batch_over = batch_more = 0
    batch_hist = {}
    worst = {}
    hist = {}
    while checked < n:
        topo, job = gen_homogeneous(rng)
        m = len(job.subjobs)
        if m < 2 or oracle(topo, job) is None:
            continue
        reg = region(topo, job)
        conts = [c for c in containers(topo, job, "region") if hall(topo, job, c)]
        if not conts:
            continue
        cont = conts[0]
        counts = classes(topo, job, cont)
        if len(counts) < 2:
            continue
        checked += 1
        bound = mtf_bound(counts)
        for order in itertools.permutations(range(m)):
            for rank, node in POLICIES:
                placement, retries, _ = mtf(topo, job, cont, order, policy(rank, node, seed))
                runs += 1
                failures += placement is None
                over += retries > bound
                key = tuple(counts)
                worst[key] = max(worst.get(key, 0), retries)
                hist[retries] = hist.get(retries, 0) + 1
                placement, batch, _ = mtf_batch(topo, job, cont, order, policy(rank, node, seed))
                batch_failures += placement is None
                batch_over += batch > bound
                batch_more += batch > retries
                batch_hist[batch] = batch_hist.get(batch, 0) + 1
    print(f"x2: {checked} feasible instances with at least two limit classes, {runs} (initial order, policy) runs")
    print(f"  move-to-front ended without a placement: {failures}; runs above the bound of Thm 5: {over}")
    print(f"  retries histogram: {dict(sorted(hist.items()))}")
    by_q = {}
    for key, value in worst.items():
        q = len(key)
        cur = by_q.get(q, (0, None, 0))
        if value > cur[0]:
            by_q[q] = (value, key, mtf_bound(list(key)))
    for q in sorted(by_q):
        value, key, bound = by_q[q]
        print(f"  {q} limit classes: most retries observed {value} (class sizes narrow->wide {key}, bound {bound})")
    tight = sum(1 for key, value in worst.items() if len(key) == 2 and value == key[0])
    two = sum(1 for key in worst if len(key) == 2)
    print(f"  two classes: bound m_narrow attained for {tight} of {two} class-size profiles")
    print(f"  prototype variant (all blocked SubJobs move together): ended without a placement {batch_failures}; "
          f"above the bound {batch_over}; more retries than the one-at-a-time variant {batch_more}")
    print(f"  prototype variant retries histogram: {dict(sorted(batch_hist.items()))}")
    assert failures == 0 and over == 0 and batch_failures == 0 and batch_over == 0


def describe(topo, job):
    doms = ", ".join(f"{d.name}(tier {d.tier}: nodes {sorted(d.nodes)})" for d in sorted(topo.domains, key=lambda d: (d.tier, d.name)))
    subs = "; ".join(f"{s.name}: tasks {list(s.tasks)}, limit {s.limit}" + (f", allowed {sorted(s.allowed)}" if s.allowed is not None else "")
                     for s in job.subjobs)
    return f"free {topo.free}; domains {doms}; SubJobs {subs}; job limit {job.limit}"


def weight(topo, job):
    return len(topo.cap) + sum(len(s.tasks) for s in job.subjobs)


def x3(n=40000, seed=3):
    """Outside class H the search is not complete: smallest counterexamples found by random search."""
    pol = policy("bestfit", "bestfit")

    # (a) SubJobs of different sizes, unit tasks, each limited to one rack.
    rng = random.Random(seed)
    feasible = sorted_miss = mtf_miss = every_order = 0
    best = best_mtf = None
    for _ in range(n):
        topo = gen_topology(rng, tiers=2, fan=(2, 3), per_rack=(1, 1), caps=(3, 4, 5, 6, 7, 8))
        m = rng.randint(3, 6)
        job = Job([SubJob(f"u{i}", (1,) * rng.randint(1, 4), 1) for i in range(m)])
        if sum(len(u.tasks) for u in job.subjobs) < sum(topo.cap) - 1 or oracle(topo, job) is None:
            continue  # keep tight instances only: loose ones are easy for any order
        feasible += 1
        root = max(topo.domains, key=lambda d: d.tier)
        big_first = sorted(range(m), key=lambda i: -len(job.subjobs[i].tasks))
        big_fails = try_job(topo, job, root, big_first, pol)[0] is None
        mtf_fails = mtf(topo, job, root, list(range(m)), pol)[0] is None
        sorted_miss += big_fails
        mtf_miss += mtf_fails
        if mtf_fails and (best_mtf is None or weight(topo, job) < weight(*best_mtf)):
            best_mtf = (topo, job)
        if big_fails and mtf_fails and all(try_job(topo, job, root, order, pol)[0] is None
                                           for order in itertools.permutations(range(m))):
            every_order += 1
            if best is None or weight(topo, job) < weight(*best):
                best = (topo, job)
    print(f"x3a: SubJobs of different sizes, limit tier 1, tight capacity: {feasible} feasible instances")
    print(f"  largest-first order fails: {sorted_miss}; move-to-front from the given order fails: {mtf_miss}; every order fails: {every_order}")
    if best_mtf:
        print(f"  smallest instance where move-to-front fails: {describe(*best_mtf)}")
    if best:
        print(f"  smallest instance where every order fails: {describe(*best)}")

    # (b) Tasks of different sizes inside one domain, no topology constraint.
    rng = random.Random(seed + 1)
    feasible = given_miss = desc_miss = 0
    best = None
    for _ in range(n):
        topo = gen_topology(rng, tiers=2, fan=(1, 1), per_rack=(2, 3), caps=(2, 3, 4, 5))
        tasks = tuple(rng.randint(1, 3) for _ in range(rng.randint(2, 5)))
        job = Job([SubJob("u0", tasks, None)])
        if oracle(topo, job) is None:
            continue
        feasible += 1
        root = max(topo.domains, key=lambda d: d.tier)
        given_miss += try_job(topo, job, root, [0], pol)[0] is None
        desc = Job([SubJob("u0", tuple(sorted(tasks, reverse=True)), None)])
        if try_job(topo, desc, root, [0], pol)[0] is None:
            desc_miss += 1
            if best is None or weight(topo, desc) < weight(*best):
                best = (topo, desc)
    print(f"x3b: tasks of different sizes in one domain: {feasible} feasible instances")
    print(f"  per-task best fit in the given order fails: {given_miss}; in largest-first order fails: {desc_miss}")
    if best:
        print(f"  smallest instance where largest-first best fit fails: {describe(*best)}")

    # (c) Same size, but each SubJob may only use its own subset of nodes (not nested).
    rng = random.Random(seed + 2)
    feasible = given_miss = mtf_miss = every_order = 0
    best = best_mtf = None
    for _ in range(n):
        topo = gen_topology(rng, tiers=2, fan=(3, 5), per_rack=(1, 1), caps=(1, 1, 2))
        nodes = list(range(len(topo.cap)))
        m = rng.randint(3, 6)
        subs = []
        for i in range(m):
            allowed = frozenset(rng.sample(nodes, rng.randint(1, min(3, len(nodes)))))
            subs.append(SubJob(f"u{i}", (1,), 1, allowed))
        job = Job(subs)
        if oracle(topo, job) is None:
            continue
        feasible += 1
        root = max(topo.domains, key=lambda d: d.tier)
        first_fails = try_job(topo, job, root, list(range(m)), pol)[0] is None
        given_miss += first_fails
        mtf_fails = mtf(topo, job, root, list(range(m)), pol)[0] is None
        mtf_miss += mtf_fails
        if mtf_fails and (best_mtf is None or weight(topo, job) < weight(*best_mtf)):
            best_mtf = (topo, job)
        if mtf_fails and all(try_job(topo, job, root, order, pol)[0] is None for order in itertools.permutations(range(m))):
            every_order += 1
            if best is None or weight(topo, job) < weight(*best):
                best = (topo, job)
    print(f"x3c: same-size SubJobs with their own admissible node sets: {feasible} feasible instances")
    print(f"  given order fails: {given_miss}; move-to-front from the given order fails: {mtf_miss}; every order fails: {every_order}")
    if best_mtf:
        print(f"  smallest instance where move-to-front fails: {describe(*best_mtf)}")
    if best:
        print(f"  smallest instance where every order fails: {describe(*best)}")


def has_matching(adj, slots):
    owner = [-1] * slots

    def augment(agent, seen):
        for v in range(slots):
            if adj[agent] >> v & 1 and not seen[v]:
                seen[v] = True
                if owner[v] < 0 or augment(owner[v], seen):
                    owner[v] = agent
                    return True
        return False

    return all(augment(a, [False] * slots) for a in range(len(adj)))


def unit_mtf(adj, prefs, order, batch=False):
    """Unit SubJobs: each takes its most preferred free admissible slot. One at a time: the
    first SubJob that finds none is moved to the front. batch: the trial runs to the end and
    every SubJob that found none moves to the front together. Returns (success, retries)."""
    order = list(order)
    seen = {tuple(order)}
    retries = 0
    while True:
        used, blocked = 0, []
        for a in order:
            for v in prefs[a]:
                if adj[a] >> v & 1 and not used >> v & 1:
                    used |= 1 << v
                    break
            else:
                blocked.append(a)
                if not batch:
                    break
        if not blocked:
            return True, retries
        order = blocked + [x for x in order if x not in blocked]
        if tuple(order) in seen:
            return False, retries
        seen.add(tuple(order))
        retries += 1


def x4(samples=100000, seed=4):
    """Theorem 6: unit SubJobs with arbitrary admissible slots (bipartite matching).
    Move-to-front must reach a full placement whenever one exists, from every initial order."""
    for agents, slots in ((2, 2), (3, 3), (3, 4), (4, 4)):
        feasible = fails = worst = batch_fails = batch_worst = 0
        prefs = [list(range(slots))] * agents
        for bits in range(1 << (agents * slots)):
            adj = [(bits >> (i * slots)) & ((1 << slots) - 1) for i in range(agents)]
            if not all(adj) or not has_matching(adj, slots):
                continue
            feasible += 1
            for order in itertools.permutations(range(agents)):
                ok, retries = unit_mtf(adj, prefs, order)
                fails += not ok
                worst = max(worst, retries)
                ok, retries = unit_mtf(adj, prefs, order, batch=True)
                batch_fails += not ok
                batch_worst = max(batch_worst, retries)
        print(f"x4: every graph with {agents} SubJobs and {slots} slots, common preference, every initial order: "
              f"{feasible} feasible graphs, move-to-front fails {fails}, most retries {worst} (2^(m-1)-1 = {2 ** (agents - 1) - 1}); "
              f"prototype variant fails {batch_fails}, most retries {batch_worst}")
        assert fails == 0 and batch_fails == 0
    rng = random.Random(seed)
    for agents, slots, common in ((5, 5, True), (6, 6, True), (8, 8, True), (6, 8, True), (4, 4, False), (5, 5, False), (6, 6, False), (8, 8, False)):
        feasible = fails = worst = batch_fails = batch_worst = 0
        for _ in range(samples):
            adj = [0] * agents
            for i in range(agents):
                for v in rng.sample(range(slots), rng.randint(1, min(3, slots))):
                    adj[i] |= 1 << v
            if not has_matching(adj, slots):
                continue
            feasible += 1
            prefs = [list(range(slots))] * agents if common else [rng.sample(range(slots), slots) for _ in range(agents)]
            order = list(range(agents))
            rng.shuffle(order)
            ok, retries = unit_mtf(adj, prefs, order)
            fails += not ok
            worst = max(worst, retries)
            ok, retries = unit_mtf(adj, prefs, order, batch=True)
            batch_fails += not ok
            batch_worst = max(batch_worst, retries)
        kind = "common preference" if common else "own preference per SubJob"
        print(f"x4: random, {agents} SubJobs, {slots} slots, {kind}: {feasible} feasible, move-to-front fails {fails}, "
              f"most retries {worst} (2^(m-1)-1 = {2 ** (agents - 1) - 1}); prototype variant fails {batch_fails}, most retries {batch_worst}")
        assert fails == 0 and batch_fails == 0
    # An explicit family with many retries: SubJob i may use slots 0..i, widest SubJob first.
    chain = []
    for agents in range(2, 17):
        adj = [(1 << (i + 1)) - 1 for i in range(agents)]
        ok, retries = unit_mtf(adj, [list(range(agents))] * agents, list(range(agents - 1, -1, -1)))
        assert ok
        chain.append(f"{agents}:{retries}")
    print(f"x4: nested chain, widest first, retries by number of SubJobs: {' '.join(chain)}")
    # Hill climbing for instances that need more retries than 2^(m-1)-1.
    for agents in (5, 6):
        best = 0
        for _ in range(300):
            adj = [rng.randrange(1, 1 << agents) for _ in range(agents)]
            if not has_matching(adj, agents):
                continue
            order = list(range(agents))
            rng.shuffle(order)
            prefs = [list(range(agents))] * agents
            cur = unit_mtf(adj, prefs, order)[1]
            for _ in range(400):
                adj2, order2 = list(adj), list(order)
                if rng.random() < 0.7:
                    i = rng.randrange(agents)
                    adj2[i] ^= 1 << rng.randrange(agents)
                    if adj2[i] == 0 or not has_matching(adj2, agents):
                        continue
                else:
                    i, j = rng.sample(range(agents), 2)
                    order2[i], order2[j] = order2[j], order2[i]
                ok, retries = unit_mtf(adj2, prefs, order2)
                assert ok
                if retries >= cur:
                    adj, order, cur = adj2, order2, retries
            best = max(best, cur)
        print(f"x4: hill climbing, {agents} SubJobs: most retries found {best} (2^(m-1)-1 = {2 ** (agents - 1) - 1})")
        assert best <= 2 ** (agents - 1) - 1


def pack(sizes, cap, racks, rule):
    """Online placement into a fixed set of racks. A job is rejected only when no rack fits it.
    best: fullest rack that fits (the bin-packing score); emptiest: emptiest rack that fits; first: lowest index."""
    loads = [0] * racks
    rejected = 0
    for x in sizes:
        fits = [i for i, load in enumerate(loads) if load + x <= cap]
        if not fits:
            rejected += 1
            continue
        if rule == "best":
            i = max(fits, key=lambda j: (loads[j], -j))
        elif rule == "emptiest":
            i = min(fits, key=lambda j: (loads[j], j))
        else:
            i = fits[0]
        loads[i] += x
    return sum(1 for load in loads if load > 0), rejected


def opt_bins(sizes, cap):
    """Exact bin packing by subset dynamic programming."""
    n = len(sizes)
    full = (1 << n) - 1
    total = [0] * (full + 1)
    for mask in range(1, full + 1):
        low = mask & -mask
        total[mask] = total[mask ^ low] + sizes[low.bit_length() - 1]
    best = [n + 1] * (full + 1)
    best[0] = 0
    for mask in range(1, full + 1):
        low = mask & -mask
        rest = mask ^ low
        sub = rest
        while True:
            if total[sub | low] <= cap:
                best[mask] = min(best[mask], best[rest ^ sub] + 1)
            if sub == 0:
                break
            sub = (sub - 1) & rest
    return best[full]


def x5(n=1500, seed=5):
    """Theorem 7: one tier, one resource, no departures, every job confined to one rack."""
    rng = random.Random(seed)
    rules = ("best", "first", "emptiest")
    worst = {r: (0, None) for r in rules}
    touched = {r: 0 for r in rules}
    rejected = {r: 0 for r in rules}
    violated = total_opt = total_racks = jobs = 0
    for _ in range(n):
        cap = rng.choice((6, 8, 10))
        sizes = [rng.randint(1, cap) for _ in range(rng.randint(4, 9))]
        opt = opt_bins(sizes, cap)
        total_opt += opt
        racks = len(sizes)          # plenty of racks: nothing is rejected
        total_racks += racks
        jobs += len(sizes)
        for rule in rules:
            used, _ = pack(sizes, cap, racks, rule)
            touched[rule] += used
            if used / opt > worst[rule][0]:
                worst[rule] = (used / opt, (cap, sizes, used, opt))
            if rule == "best" and used > math.floor(1.7 * opt):
                violated += 1
            # A cluster with exactly as many racks as the optimum needs.
            rejected[rule] += pack(sizes, cap, opt, rule)[1]
    print(f"x5: {n} random job sequences ({jobs} jobs), each job confined to one rack of equal capacity")
    print(f"  racks an optimal packing needs: {total_opt} of {total_racks}")
    for rule in rules:
        ratio, (cap, sizes, used, opt) = worst[rule]
        print(f"  {rule:8s}: racks touched {touched[rule]} (empty racks left {total_racks - touched[rule]}); "
              f"worst touched/optimum {used}/{opt} = {ratio:.2f} (capacity {cap}, sizes {sizes}); "
              f"jobs rejected when the cluster has exactly the optimal number of racks: {rejected[rule]}")
    print(f"  best fit above floor(1.7 * OPT): {violated}")
    assert violated == 0


def x6(seed=6):
    """Proposition 9: with a fresh tie order per failed cycle, waiting time is geometric."""
    rng = random.Random(seed)
    for good, ties in ((1, 2), (1, 4), (2, 8)):
        p = good / ties
        rounds = []
        for _ in range(20000):
            r = 1
            while rng.randrange(ties) >= good:
                r += 1
            rounds.append(r)
        mean = sum(rounds) / len(rounds)
        tail = sum(1 for r in rounds if r > 9) / len(rounds)
        print(f"x6: {good} good of {ties} tied choices: mean cycles {mean:.2f} (1/p = {1 / p:.2f}); "
              f"P(more than 9 cycles) {tail:.4f} ((1-p)^9 = {(1 - p) ** 9:.4f})")


if __name__ == "__main__":
    todo = sys.argv[1:] or ["x1", "x2", "x3", "x4", "x5", "x6"]
    for name in todo:
        start = time.time()
        globals()[name]()
        print(f"  [{name} took {time.time() - start:.1f}s]\n")
