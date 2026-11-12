/*
Round-2 proposal-analyst probes for issue 004 (review only, not part of the
proposal's own evidence). Needs probes/common, history, prototype, prototype2,
prototype2-ext and the round-2 prototype worktree.
*/

package allocate

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	v1 "k8s.io/api/core/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// raRun is runProbe plus a hook that sees the session after the action ran
// and before it is closed.
func raRun(tb testing.TB, tc *uthelper.TestCommonStruct, tiers []conf.Tier, extra map[string]framework.PluginBuilder,
	action *Action, inspect func(ssn *framework.Session)) *probeResult {
	tb.Helper()
	res := &probeResult{
		counters:       &probeCounters{},
		placed:         map[string]string{},
		jobFitErrors:   map[api.JobID]string{},
		fitError:       map[api.JobID]string{},
		nodesFitErrors: map[api.JobID]map[string]string{},
	}
	tc.Plugins = probePluginBuilders(res.counters, extra)
	ssn := tc.RegisterSession(tiers, nil)
	defer tc.Close()
	tc.Run([]framework.Action{action})
	for jobID, job := range ssn.Jobs {
		for _, task := range job.Tasks {
			if task.Pod == nil || task.Pod.Status.Phase != v1.PodPending {
				continue
			}
			if task.Status == api.Pending || task.NodeName == "" {
				continue
			}
			res.placed[task.Name] = task.NodeName
		}
		res.jobFitErrors[jobID] = job.JobFitErrors
		res.fitError[jobID] = job.FitError()
		perTask := map[string]string{}
		for taskID, fitErrors := range job.NodesFitErrors {
			if fitErrors == nil {
				continue
			}
			name := string(taskID)
			if task := job.Tasks[taskID]; task != nil {
				name = task.Name
			}
			perTask[name] = fitErrors.Error()
		}
		res.nodesFitErrors[jobID] = perTask
	}
	if inspect != nil {
		inspect(ssn)
	}
	return res
}

// unmodifiedAction is the allocate action with every prototype switch off.
func unmodifiedAction() *Action {
	a := New()
	a.rankThenVerify, a.fitBound, a.deterministicTies = false, false, false
	a.repairBudget, a.repairPerLayer, a.repairOrder = 0, false, ""
	return a
}

func subGroupWorkerPolicy(minGroups int32) schedulingv1.SubGroupPolicySpec {
	size, tier := int32(1), 1
	return schedulingv1.SubGroupPolicySpec{
		Name: "worker", MatchLabelKeys: []string{"idx"}, SubGroupSize: &size, MinSubGroups: &minGroups,
		NetworkTopology: &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier},
	}
}

// ---- A1 -------------------------------------------------------------------
// Racks a and b are equally used (2 of 4 CPU free each). The flexible SubJob
// (handled first) fits either; the pinned SubJob only fits rack a and needs
// all that is free there. The flexible SubJob's choice between a and b is a
// tie.
func tieContentionScenario(job string) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0), nodeName(0, 1, 0)}, "2", "1Gi")
	pg := util.BuildPodGroup(job, "c1", "q1", 2, nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{subGroupWorkerPolicy(2)}
	flexible := util.BuildPod("c1", job+"-flexible", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": "0"}, nil)
	pinned := util.BuildPod("c1", job+"-pinned", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "1"}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "tie-contention", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
		Pods: append([]*v1.Pod{flexible, pinned}, fillers...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// A1: today's random tie-break makes this Job schedulable in a fraction of
// the cycles (so it schedules after a few cycles). With deterministic ties
// and no repair the outcome is fixed per Job name.
func TestReviewA1RandomTieIsAnImplicitRestart(t *testing.T) {
	const names, repeats = 16, 10
	type cfg struct {
		name   string
		action func() *Action
	}
	configs := []cfg{
		{"unmodified (random ties)", unmodifiedAction},
		{"exhaustive + deterministic ties", func() *Action { return modeExhaustiveDet.action() }},
		{"ranked + fitBound (round 1)", func() *Action { return modeRankedFit.action() }},
		{"ranked + fitBound + repair(4)", func() *Action { a := modeRankedFit.action(); a.repairBudget = 4; return a }},
	}
	t.Logf("| configuration | job names always scheduled | never scheduled | sometimes | sessions scheduled / total |")
	t.Logf("|---|---|---|---|---|")
	for _, c := range configs {
		always, never, sometimes, ok := 0, 0, 0, 0
		var neverNames []string
		for j := 0; j < names; j++ {
			job := fmt.Sprintf("job%02d", j)
			succ := 0
			for rep := 0; rep < repeats; rep++ {
				res := runProbe(t, tieContentionScenario(job), probeTiers(), nil, c.action(), nil)
				switch got := res.count(job + "-"); got {
				case 2:
					succ++
				case 0:
				default:
					t.Errorf("%s %s: partial gang placed (%d/2)", c.name, job, got)
				}
			}
			ok += succ
			switch succ {
			case repeats:
				always++
			case 0:
				never++
				neverNames = append(neverNames, job)
			default:
				sometimes++
			}
		}
		t.Logf("| %s | %d | %d | %d | %d/%d |", c.name, always, never, sometimes, ok, names*repeats)
		if len(neverNames) > 0 {
			t.Logf("    never scheduled: %s", strings.Join(neverNames, " "))
		}
	}
}

// ---- A2 -------------------------------------------------------------------
// Elastic gang: minMember 2 of 3 single-pod SubGroups. Rack a has 2 CPU free,
// rack b 4. The flexible SubJob needs 2 CPU anywhere, the two pinned SubJobs
// need 1 CPU each in rack a.
func elasticContentionScenario(flexibleFirst bool) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0)}, "2", "1Gi")
	pg := util.BuildPodGroup("job", "c1", "q1", 2, nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{subGroupWorkerPolicy(2)}
	idx := []string{"0", "1", "2"} // flexible, pinned1, pinned2
	if !flexibleFirst {
		idx = []string{"2", "0", "1"}
	}
	flexible := util.BuildPod("c1", "job-flexible", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": idx[0]}, nil)
	pinned1 := util.BuildPod("c1", "job-pinned1", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": idx[1]}, map[string]string{"only": "a"})
	pinned2 := util.BuildPod("c1", "job-pinned2", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": idx[2]}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "elastic-contention", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
		Pods: append([]*v1.Pod{flexible, pinned1, pinned2}, fillers...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// A2: does a SubJob decision recorded by the failed normal dry-run leak into
// the committed state when repair succeeds in the same container?
func TestReviewA2StaleSubJobDecisionAfterRepair(t *testing.T) {
	type run struct {
		name          string
		flexibleFirst bool
		budget        int
	}
	for _, r := range []run{
		{"flexible first, repair off", true, 0},
		{"flexible first, repair(4)", true, 4},
		{"pinned first (control: normal search succeeds), repair(4)", false, 4},
	} {
		for _, mode := range []protoMode{modeExhaustiveDet, modeRankedFit} {
			action := mode.action()
			action.repairBudget = r.budget
			var state []string
			res := raRun(t, elasticContentionScenario(r.flexibleFirst), probeTiers(), nil, action, func(ssn *framework.Session) {
				job := ssn.Jobs["c1/job"]
				if job == nil {
					return
				}
				state = append(state, fmt.Sprintf("job.AllocatedHyperNode=%q", job.AllocatedHyperNode))
				ids := make([]string, 0, len(job.SubJobs))
				for id := range job.SubJobs {
					ids = append(ids, string(id))
				}
				sort.Strings(ids)
				for _, id := range ids {
					subJob := job.SubJobs[api.SubJobID(id)]
					var tasks []string
					for _, task := range subJob.Tasks {
						tasks = append(tasks, fmt.Sprintf("%s:%s@%s", task.Name, task.Status, task.NodeName))
					}
					sort.Strings(tasks)
					state = append(state, fmt.Sprintf("%s AllocatedHyperNode=%q tasks=%v", id, subJob.AllocatedHyperNode, tasks))
				}
			})
			t.Logf("%s / %s: placed %d/3 %v", r.name, mode.name, res.count("job-"), res.placed)
			for _, repair := range action.trace.Repairs {
				t.Logf("    %s", repair)
			}
			for _, line := range state {
				t.Logf("    %s", line)
			}
			t.Logf("    fitError: %s", shortFitError(res.fitError["c1/job"]))
		}
	}
}

// ---- A3 -------------------------------------------------------------------
// Strict gang of three that has no legal placement: two pinned SubJobs need
// 2 CPU each in rack a, which has 2 CPU free.
func infeasibleContentionScenario() *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0)}, "2", "1Gi")
	pg := util.BuildPodGroup("job", "c1", "q1", 3, nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{subGroupWorkerPolicy(3)}
	flexible := util.BuildPod("c1", "job-flexible", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": "0"}, nil)
	pinned1 := util.BuildPod("c1", "job-pinned1", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "1"}, map[string]string{"only": "a"})
	pinned2 := util.BuildPod("c1", "job-pinned2", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "2"}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "infeasible-contention", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
		Pods: append([]*v1.Pod{flexible, pinned1, pinned2}, fillers...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// A3: what a user sees for a Job that is unschedulable with and without repair.
func TestReviewA3FailureDiagnosticsWithRepair(t *testing.T) {
	for _, budget := range []int{0, 4, 16} {
		action := modeRankedFit.action()
		action.repairBudget = budget
		res := runProbe(t, infeasibleContentionScenario(), probeTiers(), nil, action, nil)
		t.Logf("repairBudget=%d placed %d/3; repair attempts=%d; flagged search-incomplete=%v", budget, res.count("job-"),
			len(action.trace.Repairs), action.trace.RepairBudgetExhausted)
		t.Logf("    fitError: %s", shortFitError(res.fitError["c1/job"]))
		tasks := make([]string, 0)
		for task, msg := range res.nodesFitErrors["c1/job"] {
			tasks = append(tasks, task+": "+msg)
		}
		sort.Strings(tasks)
		for _, line := range tasks {
			t.Logf("    task fit error: %s", line)
		}
		for _, repair := range action.trace.Repairs {
			t.Logf("    %s", repair)
		}
	}
}

// ---- A5 -------------------------------------------------------------------

const orderPluginName = "review-order-recorder"

type orderRecorder struct {
	mu    sync.Mutex
	order []string
	seen  map[string]bool
}

type orderPlugin struct{ r *orderRecorder }

func (p *orderPlugin) Name() string { return orderPluginName }

func (p *orderPlugin) OnSessionOpen(ssn *framework.Session) {
	ssn.AddPrePredicateFn(p.Name(), func(task *api.TaskInfo) error {
		p.r.mu.Lock()
		defer p.r.mu.Unlock()
		if !p.r.seen[string(task.Job)] {
			p.r.seen[string(task.Job)] = true
			p.r.order = append(p.r.order, string(task.Job))
		}
		return nil
	})
}

func (p *orderPlugin) OnSessionClose(*framework.Session) {}

// A5: R6 reports Jobs that are scheduled today and not with region + repair.
// Check case by case that each of them is handled after a Job that is newly
// scheduled in the same session.
func TestReviewA5DisplacedJobsComeAfterGainedJobs(t *testing.T) {
	const scenarios = 600
	runWithOrder := func(seed int64, mode protoMode, r round2, order string) (map[string]bool, []string) {
		scenario, names := genMultiJobScenario(seed)
		previous := framework.ProbeScopeUnion
		framework.ProbeScopeUnion = r.scope
		defer func() { framework.ProbeScopeUnion = previous }()
		action := mode.action()
		action.repairBudget, action.repairOrder = r.budget, order
		tiers, extra := historyTiers()
		enabled := true
		tiers[0].Plugins = append(tiers[0].Plugins, conf.PluginOption{Name: orderPluginName, EnabledPredicate: &enabled})
		recorder := &orderRecorder{seen: map[string]bool{}}
		extra[orderPluginName] = func(framework.Arguments) framework.Plugin { return &orderPlugin{r: recorder} }
		res := runProbe(t, scenario.tc, tiers, extra, action, nil)
		scheduled := map[string]bool{}
		for pod := range res.placed {
			for _, name := range names {
				if strings.HasPrefix(pod, name+"-") {
					scheduled[name] = true
				}
			}
		}
		return scheduled, recorder.order
	}
	displacedTotal, explained, unexplained, noOrder := 0, 0, 0, 0
	for seed := int64(1); seed <= scenarios; seed++ {
		_, names := genMultiJobScenario(seed)
		current, _ := runWithOrder(seed, modeExhaustiveDet, round2{}, "")
		now, order := runWithOrder(seed, modeRankedFit, round2{scope: true, budget: 4}, "reserve")
		position := map[string]int{}
		for i, job := range order {
			position[strings.TrimPrefix(job, "c1/")] = i
		}
		for _, name := range names {
			if !current[name] || now[name] {
				continue
			}
			displacedTotal++
			pos, found := position[name]
			if !found {
				noOrder++
				t.Logf("seed %d: displaced job %s has no recorded task attempt in the new run (order %v)", seed, name, order)
				continue
			}
			ok := false
			for _, other := range names {
				if !current[other] && now[other] {
					if otherPos, otherFound := position[other]; otherFound && otherPos < pos {
						ok = true
					}
				}
			}
			if ok {
				explained++
			} else {
				unexplained++
				t.Errorf("seed %d: job %s lost its place but no newly scheduled job was handled before it (order %v, current %v, now %v)",
					seed, name, order, current, now)
			}
		}
	}
	t.Logf("%d sessions: %d displaced jobs; %d handled after a newly scheduled job; %d not explained; %d without recorded order",
		scenarios, displacedTotal, explained, unexplained, noOrder)
}

// ---- A6 -------------------------------------------------------------------

// A6: R4 and R5 at the recommended default (budget 4, one attempt reserved),
// plus two questions the proposal's tables do not answer: how many repair
// attempts ended with nothing blocked (they still cost one unit of budget), and
// how many Jobs with a legal placement stay pending without being flagged
// "search incomplete".
func TestReviewA6DefaultBudgetAndIncompleteFlag(t *testing.T) {
	type cfg struct {
		name  string
		mode  protoMode
		r     round2
		order string
	}
	type tally struct {
		scheduled, missed, repairs, emptyAttempts, exhausted   int
		missedFlagged, missedRepairedUnflagged, missedNoRepair int
		attemptsCurrentFails                                   int64
	}
	report := func(title string, scenarios int, gen func(seed int64) *singleJobScenario, configs []cfg) {
		tallies := make([]tally, len(configs))
		feasible := 0
		for seed := int64(1); seed <= int64(scenarios); seed++ {
			problem := extractOracleProblem(gen(seed).tc)
			best := problem.minTier()
			if best > 0 {
				feasible++
			}
			var current map[string]string
			for i, c := range configs {
				scenario := gen(seed)
				res, action := runRound2(t, scenario.tc, c.mode, c.r, func(a *Action) { a.repairOrder = c.order }, nil)
				placement := jobPlacement(res)
				if msg := illegalPlacement(scenario.tc, placement); msg != "" {
					t.Errorf("%s seed %d %s: illegal placement: %s", title, seed, c.name, msg)
				}
				if len(placement) > 0 && len(placement) != scenario.pods {
					t.Errorf("%s seed %d %s: partial gang placed: %d of %d pods", title, seed, c.name, len(placement), scenario.pods)
				}
				if i == 0 {
					current = placement
				}
				tl := &tallies[i]
				tl.repairs += len(action.trace.Repairs)
				tl.exhausted += len(action.trace.RepairBudgetExhausted)
				for _, repair := range action.trace.Repairs {
					if !repair.Success && len(repair.Blocked) == 0 {
						tl.emptyAttempts++
					}
				}
				if len(current) == 0 {
					tl.attemptsCurrentFails += res.counters.prePredicate.Load()
				}
				switch {
				case len(placement) > 0:
					tl.scheduled++
				case best > 0:
					tl.missed++
					switch {
					case len(action.trace.RepairBudgetExhausted) > 0:
						tl.missedFlagged++
					case len(action.trace.Repairs) > 0:
						tl.missedRepairedUnflagged++
					default:
						tl.missedNoRepair++
					}
				}
				if len(current) > 0 && len(placement) == 0 {
					t.Errorf("%s seed %d %s: schedulable today, not scheduled now", title, seed, c.name)
				}
			}
		}
		t.Logf("%s: %d scenarios, oracle finds a legal placement for %d", title, scenarios, feasible)
		t.Logf("| configuration | scheduled | missed | missed and flagged search-incomplete | missed, repair ran, not flagged | missed, no repair ran, not flagged | repair attempts | failed attempts with no blocked SubJob | budget exhausted | task attempts, current fails |")
		t.Logf("|---|---|---|---|---|---|---|---|---|---|")
		for i, c := range configs {
			tl := tallies[i]
			t.Logf("| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d |", c.name, tl.scheduled, tl.missed, tl.missedFlagged,
				tl.missedRepairedUnflagged, tl.missedNoRepair, tl.repairs, tl.emptyAttempts, tl.exhausted, tl.attemptsCurrentFails)
		}
	}
	r4 := func(seed int64) *singleJobScenario {
		scenario := genSingleJobScenario(seed)
		mixSizes(scenario, seed)
		return scenario
	}
	report("R4 scenarios", 1200, r4, []cfg{
		{"current search", modeExhaustiveDet, round2{}, ""},
		{"ranked + region + repair(4) [proposal row, expect 7 missed]", modeRankedFit, round2{scope: true, budget: 4}, ""},
		{"ranked + region + repair(4, reserve) [recommended default]", modeRankedFit, round2{scope: true, budget: 4}, "reserve"},
		{"ranked + region + repair(8, reserve)", modeRankedFit, round2{scope: true, budget: 8}, "reserve"},
	})
	report("R5 scenarios", 1000, genContentionScenario, []cfg{
		{"current search", modeExhaustiveDet, round2{}, ""},
		{"ranked + repair(2, reserve)", modeRankedFit, round2{budget: 2}, "reserve"},
		{"ranked + repair(4, reserve) [recommended default]", modeRankedFit, round2{budget: 4}, "reserve"},
	})
}
