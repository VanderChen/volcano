"""Abstract model of the filter -> rank -> verify search of issue 004.

The model is independent of the Volcano code. It exists to check the theorems
in THEORY.md by brute force on small instances.

Objects
  Topology  a tree of domains with tiers; leaves are nodes with integer free capacity.
  SubJob    a list of task demands, a tier limit, and an optional set of admissible nodes.
  Job       SubJobs, a job tier limit, and tier-t domains excluded by required anti-affinity.

Two readings of the exclusion constraint
  "region"     the excluded domains remove nodes; every domain within the job limit that
               still has a usable node is a container (the design).
  "container"  only non-excluded domains at or below the comparison tier are containers
               (the current behavior).
"""
from __future__ import annotations

import random
from dataclasses import dataclass, field
from functools import lru_cache


@dataclass(frozen=True)
class Domain:
    name: str
    tier: int
    nodes: frozenset


@dataclass
class Topology:
    cap: list
    free: list
    domains: list
    top: int

    def by_name(self, name):
        return next(d for d in self.domains if d.name == name)


@dataclass(frozen=True)
class SubJob:
    name: str
    tasks: tuple
    limit: int | None = None          # None: no own limit, placed in the job's container
    allowed: frozenset | None = None  # None: every node


@dataclass
class Job:
    subjobs: list
    limit: int | None = None
    excluded: frozenset = frozenset()  # names of excluded domains
    cmp_tier: int | None = None        # comparison tier of the required anti-affinity term, None: no term


@dataclass
class Policy:
    rank: str = "bestfit"   # bestfit | emptiest | name | random
    node: str = "bestfit"   # bestfit | emptiest | random
    rng: random.Random = field(default_factory=lambda: random.Random(0))


def gen_topology(rng, tiers=None, fan=(1, 3), per_rack=(1, 3), caps=(0, 1, 2, 3)):
    top = tiers or rng.choice((2, 3))
    cap, domains, counter = [], [], [0]

    def build(tier):
        counter[0] += 1
        name = f"t{tier}.{counter[0]}"
        if tier == 1:
            ids = []
            for _ in range(rng.randint(*per_rack)):
                cap.append(rng.choice(caps))
                ids.append(len(cap) - 1)
            nodes = frozenset(ids)
        else:
            nodes = frozenset()
            for _ in range(rng.randint(*fan)):
                nodes |= build(tier - 1)
        domains.append(Domain(name, tier, nodes))
        return nodes

    build(top)
    return Topology(cap=list(cap), free=list(cap), domains=domains, top=top)


def region(topo, job):
    banned = set()
    for d in topo.domains:
        if d.name in job.excluded:
            banned |= d.nodes
    return frozenset(range(len(topo.cap))) - banned


def usable(domain, sub, reg):
    nodes = domain.nodes & reg
    return nodes if sub is None or sub.allowed is None else nodes & sub.allowed


def job_limit(topo, job):
    return topo.top if job.limit is None else min(job.limit, topo.top)


def containers(topo, job, semantics):
    """Job-level candidates, grouped by tier from narrow to wide."""
    reg = region(topo, job)
    limit = job_limit(topo, job)
    out = []
    for d in topo.domains:
        if d.tier > limit or not (d.nodes & reg):
            continue
        if semantics == "container" and job.cmp_tier is not None:
            # Current behavior: a domain above the comparison tier is not a legal candidate
            # for a required term, and an excluded domain is dropped by name.
            if d.tier > job.cmp_tier or not d.nodes <= reg:
                continue
        out.append(d)
    return sorted(out, key=lambda d: (d.tier, d.name))


def subjob_candidates(topo, container, sub):
    if sub.limit is None:
        return [container]
    limit = min(sub.limit, container.tier)
    return [d for d in topo.domains if d.nodes <= container.nodes and d.tier <= limit]


def score(topo, free, nodes, demand):
    alloc = sum(topo.cap[n] for n in nodes)
    spare = sum(free[n] for n in nodes)
    if alloc == 0 or demand > spare:
        return 0.0
    return (alloc - spare + demand) / alloc


def ranked(topo, free, cands, node_sets, demand, policy):
    """Order candidates: tier first, then the rank policy, then name."""
    def key(d):
        nodes = node_sets[d.name]
        if policy.rank == "bestfit":
            second = -score(topo, free, nodes, demand)
        elif policy.rank == "emptiest":
            second = -sum(free[n] for n in nodes)
        elif policy.rank == "random":
            second = policy.rng.random()
        else:
            second = 0
        return (d.tier, second, d.name)
    return sorted(cands, key=key)


def verify(free, tasks, nodes, policy):
    """Place tasks one by one on any node that fits. Returns the node of each task or None."""
    free = list(free)
    placed = []
    for demand in tasks:
        fits = [n for n in sorted(nodes) if free[n] >= demand]
        if not fits:
            return None, None
        if policy.node == "bestfit":
            n = min(fits, key=lambda x: (free[x], x))
        elif policy.node == "emptiest":
            n = max(fits, key=lambda x: (free[x], -x))
        else:
            n = policy.rng.choice(fits)
        free[n] -= demand
        placed.append(n)
    return free, placed


def try_job(topo, job, container, order, policy, stats=None):
    """One Job-level trial placement inside a container. Returns (placement, blocked, placed_any)."""
    reg = region(topo, job)
    free = list(topo.free)
    placement = {}
    for position, index in enumerate(order):
        sub = job.subjobs[index]
        cands = subjob_candidates(topo, container, sub)
        node_sets = {d.name: usable(d, sub, reg) for d in cands}
        cands = [d for d in cands if node_sets[d.name]]
        done = False
        for d in ranked(topo, free, cands, node_sets, sum(sub.tasks), policy):
            if stats is not None:
                stats["verify"] = stats.get("verify", 0) + 1
            new_free, nodes = verify(free, sub.tasks, node_sets[d.name], policy)
            if new_free is not None:
                free, placement[index], done = new_free, nodes, True
                break
        if not done:
            return None, index, position > 0
    return placement, None, True


def run_all(topo, job, container, order, policy):
    """One Job-level trial placement that does not stop at the first failure: a SubJob that
    cannot be placed is skipped. Returns (placement or None, SubJobs that failed after
    something had been placed, in the order they were met). This is what the prototype does."""
    reg = region(topo, job)
    free = list(topo.free)
    placement, blocked, failed = {}, [], False
    for index in order:
        sub = job.subjobs[index]
        cands = subjob_candidates(topo, container, sub)
        node_sets = {d.name: usable(d, sub, reg) for d in cands}
        cands = [d for d in cands if node_sets[d.name]]
        for d in ranked(topo, free, cands, node_sets, sum(sub.tasks), policy):
            new_free, nodes = verify(free, sub.tasks, node_sets[d.name], policy)
            if new_free is not None:
                free, placement[index] = new_free, nodes
                break
        else:
            failed = True
            if placement:
                blocked.append(index)
    return (None if failed else placement), blocked


def mtf_batch(topo, job, container, order, policy, limit=10_000):
    """The prototype's retry: every SubJob blocked in a trial moves to the front together,
    keeping the order in which they were met. Returns (placement, retries, final order)."""
    order = list(order)
    seen = {tuple(order)}
    retries = 0
    while True:
        placement, blocked = run_all(topo, job, container, order, policy)
        if placement is not None:
            return placement, retries, order
        if not blocked:
            return None, retries, order
        order = blocked + [i for i in order if i not in blocked]
        if tuple(order) in seen or retries >= limit:
            return None, retries, order
        seen.add(tuple(order))
        retries += 1


def mtf(topo, job, container, order, policy, limit=10_000):
    """Move-to-front retry inside one container until success or a repeated order.
    Returns (placement, retries, final order)."""
    order = list(order)
    seen = {tuple(order)}
    retries = 0
    while True:
        placement, blocked, _ = try_job(topo, job, container, order, policy)
        if placement is not None:
            return placement, retries, order
        order = [blocked] + [i for i in order if i != blocked]
        if tuple(order) in seen or retries >= limit:
            return None, retries, order
        seen.add(tuple(order))
        retries += 1


def search(topo, job, order, policy, semantics="region", budget=0, stats=None):
    """The full search: containers from narrow to wide, then the bounded retry of the design.
    Returns (placement, container tier, retries used)."""
    reg = region(topo, job)
    conts = containers(topo, job, semantics)
    node_sets = {d.name: d.nodes & reg for d in conts}
    demand = sum(sum(s.tasks) for s in job.subjobs)
    blocked = []
    for d in ranked(topo, topo.free, conts, node_sets, demand, policy):
        placement, who, placed_any = try_job(topo, job, d, order, policy, stats)
        if placement is not None:
            return placement, d.tier, 0
        if placed_any:
            blocked.append((d, who))
    used = 0
    seen_nodes = set()
    todo = []
    for d, who in blocked:
        # Gate of the design: a container whose usable free capacity is below the total
        # request cannot hold a strict gang, so it is skipped without using the budget.
        if sum(topo.free[n] for n in node_sets[d.name]) < demand:
            continue
        if node_sets[d.name] not in seen_nodes:
            seen_nodes.add(node_sets[d.name])
            todo.append((d, who))
    for position, (d, who) in enumerate(todo):
        last = position == len(todo) - 1
        cur = list(order)
        seen = {tuple(cur)}
        while used < budget - (0 if last else 1):
            cur = [who] + [i for i in cur if i != who]
            if tuple(cur) in seen:
                break
            seen.add(tuple(cur))
            placement, who2, placed_any = try_job(topo, job, d, cur, policy, stats)
            if placement is not None:
                return placement, d.tier, used + 1
            if not placed_any:
                break
            used += 1
            who = who2
    return None, None, used


def oracle(topo, job):
    """Exact answer by exhaustive enumeration. Returns the narrowest container tier or None.
    It follows the definition of a legal placement and uses none of the search code."""
    reg = region(topo, job)
    limit = job_limit(topo, job)
    best = None
    for c in sorted(topo.domains, key=lambda d: d.tier):
        if c.tier > limit or (best is not None and c.tier >= best):
            continue
        if _placeable(topo, job, c, reg):
            best = c.tier
    return best


def _placeable(topo, job, container, reg):
    subs = job.subjobs
    hosts = []
    for sub in subs:
        if sub.limit is None:
            cands = [container]
        else:
            cands = [d for d in topo.domains if d.nodes <= container.nodes and d.tier <= sub.limit]
        # A maximal host is enough: a placement inside a domain is also inside its ancestors.
        sets = {usable(d, sub, reg) for d in cands}
        hosts.append([s for s in sets if s and not any(s < other for other in sets)])

    @lru_cache(maxsize=None)
    def rec(index, free):
        if index == len(subs):
            return True
        for nodes in hosts[index]:
            for after in _assignments(tuple(sorted(subs[index].tasks, reverse=True)), tuple(sorted(nodes)), free):
                if rec(index + 1, after):
                    return True
        return False

    return rec(0, tuple(topo.free))


def _assignments(tasks, nodes, free):
    """All distinct free vectors after placing the tasks on the given nodes."""
    results = set()

    def rec(i, free, floor):
        if i == len(tasks):
            results.add(free)
            return
        same = i > 0 and tasks[i] == tasks[i - 1]
        for pos, n in enumerate(nodes):
            if same and pos < floor:
                continue
            if free[n] >= tasks[i]:
                nxt = list(free)
                nxt[n] -= tasks[i]
                rec(i + 1, tuple(nxt), pos)

    rec(0, free, 0)
    return results


def hall(topo, job, container):
    """The counting condition of Theorem 4 for the homogeneous class (same task, same SubJob size)."""
    reg = region(topo, job)
    demand = job.subjobs[0].tasks[0]
    size = len(job.subjobs[0].tasks)
    limits = sorted({min(s.limit or container.tier, container.tier) for s in job.subjobs})
    for h in limits:
        inside = [d for d in topo.domains if d.nodes <= container.nodes and d.tier <= h]
        maximal = [d for d in inside if not any(d.nodes < e.nodes for e in inside)]
        seen, cap = set(), 0
        for d in maximal:
            if d.nodes in seen:
                continue
            seen.add(d.nodes)
            cap += sum(topo.free[n] // demand for n in d.nodes & reg) // size
        need = sum(1 for s in job.subjobs if min(s.limit or container.tier, container.tier) <= h)
        if need > cap:
            return False
    return True


def hall_tier(topo, job):
    reg = region(topo, job)
    for c in sorted(topo.domains, key=lambda d: d.tier):
        if c.tier <= job_limit(topo, job) and (c.nodes & reg) and hall(topo, job, c):
            return c.tier
    return None
