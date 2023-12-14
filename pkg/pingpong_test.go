// Install Hello World in a 1-1 topology; front-end on pub,
// backend on prv.  Add a new skupper node on a third
// namespace and move part of hello world there.  Once
// good, remove the same from the original namespace (app
// and Skupper).  Validate all good, and move back.
//
// repeat it a few times (or 90% of the alloted test time)
//
// Options:
//
// TODO
//   - remove service first
//   - remove link first
//   - skupper delete, direct
//   - or remove the target deployment
//   - Consider changing the retry configurations by KeepTrying with a
//     context that has a deadline close to the test's.
//
// By default, use a different one each time, but allow
// for selecting a single one
package pingpong

import (
	"log"
	"os"
	"testing"
	"time"

	frame2 "github.com/hash-d/frame2/pkg"
	"github.com/hash-d/frame2/pkg/composite"
	"github.com/hash-d/frame2/pkg/deploy"
	"github.com/hash-d/frame2/pkg/disruptors"
	"github.com/hash-d/frame2/pkg/environment"
	"github.com/hash-d/frame2/pkg/execute"
	"github.com/hash-d/frame2/pkg/skupperexecute"
	"github.com/hash-d/frame2/pkg/topology"
	"github.com/hash-d/frame2/pkg/topology/topologies"
	"github.com/skupperproject/skupper/test/utils/base"
	"gotest.tools/assert"
)

var runner = &base.ClusterTestRunnerBase{}

// Installs HelloWorld front and backend on on the left branch of a V-shaped topology, and then
// migrates it to the right branch and back
func TestPingPong(t *testing.T) {
	r := frame2.Run{
		T: t,
	}
	defer r.Report()
	defer r.Finalize()
	r.AllowDisruptors([]frame2.Disruptor{
		&disruptors.NoHttp{},
		&disruptors.DeploymentConfigBlindly{},
		&disruptors.MixedVersionVan{},
		&disruptors.UpgradeAndFinalize{},
		&disruptors.NoConsole{},
		&disruptors.NoFlowCollector{},
	})

	var topologyV topology.Basic
	topologyV = &topologies.V{
		Name:           "pingpong",
		TestRunnerBase: runner,
		EmptyRight:     true,
	}

	setup := frame2.Phase{
		Runner: &r,
		Setup: []frame2.Step{
			{
				Doc: "Setup a HelloWorld environment",
				Modify: &environment.HelloWorld{
					Topology:      &topologyV,
					AutoTearDown:  true,
					SkupperExpose: true,
				},
			},
		},
	}
	assert.Assert(t, setup.Run())

	var topo topology.TwoBranched = topologyV.(topology.TwoBranched)
	vertex, err := topo.GetVertex()
	assert.Assert(t, err)
	rightFront, err := topo.GetRight(topology.Public, 1)
	assert.Assert(t, err)
	leftBack, err := topo.GetLeft(topology.Private, 1)
	assert.Assert(t, err)
	leftFront, err := topo.GetLeft(topology.Public, 1)
	assert.Assert(t, err)
	rightBack, err := topo.GetRight(topology.Private, 1)
	assert.Assert(t, err)

	monitorPhase := frame2.Phase{
		Runner: &r,
		// TODO there are two options here: put it on MainSteps or Setup.  On Setup,
		// the monitor gets its AutoTearDown; on MainSteps, the monitor gets instaled
		// even if the Setup was skipped.  As we do no have Setup skipping yet, let's
		// use the AutoTearDown.
		//
		// It's also out of the loop, so we install it only once.
		Setup: []frame2.Step{
			{
				// Our validations will run from the vertex node; before we
				// start monitoring, let's make sure it looks good
				Doc: "Validate Hello World deployment from vertex",
				Validator: &deploy.HelloWorldValidate{
					Namespace: vertex,
				},
				ValidatorRetry: frame2.RetryOptions{
					Ignore:     5,
					Ensure:     5,
					KeepTrying: true,
					Timeout:    time.Minute * 30,
				},
				ValidatorFinal: true,
			}, {

				Doc: "Installing hello-world monitors",
				Modify: &frame2.DefaultMonitor{
					Validators: map[string]frame2.Validator{
						"hello-world": &deploy.HelloWorldValidate{
							Namespace: vertex,
						},
					},
				},
			},
		},
	}
	assert.Assert(t, monitorPhase.Run())

	deltas := []time.Duration{}

	for {
		startTime := time.Now()
		main := frame2.Phase{
			Runner: &r,
			MainSteps: []frame2.Step{
				{
					Modify: &skupperexecute.CliSkupper{
						Args:           []string{"network", "status"},
						ClusterContext: vertex,
						Cmd: execute.Cmd{
							ForceOutput: true,
						},
					},
					SkipWhen: true,
				}, {
					Name: "Move to right",
					Modify: &MoveToRight{
						Topology:   topologyV.(topology.TwoBranched),
						LeftFront:  leftFront,
						LeftBack:   leftBack,
						RightFront: rightFront,
						RightBack:  rightBack,
						Vertex:     vertex,
					},
				}, {
					Modify: &skupperexecute.CliSkupper{
						Args:           []string{"network", "status"},
						ClusterContext: vertex,
						Cmd: execute.Cmd{
							ForceOutput: true,
						},
					},
					SkipWhen: true,
				}, {
					Name: "Move to left",
					Modify: &MoveToLeft{
						Topology:   topologyV.(topology.TwoBranched),
						LeftFront:  leftFront,
						LeftBack:   leftBack,
						RightFront: rightFront,
						RightBack:  rightBack,
						Vertex:     vertex,
					},
				},
			},
		}
		assert.Assert(t, main.Run())

		// Move all the log below to a new Executor: GreedyRepeatedTester
		endTime := time.Now()

		delta := endTime.Sub(startTime)
		deltas = append(deltas, delta)

		testDeadline, ok := t.Deadline()
		if !ok {
			// No deadline, and we do not want to loop forever
			break
		}
		var maxTime time.Duration
		var totalDuration time.Duration

		for _, d := range deltas {
			totalDuration += d
			if d > maxTime {
				maxTime = d
			}
		}

		if testDeadline.Sub(time.Now()) < maxTime*2 {
			log.Printf("Finishing Pingpong test after %d run(s)", len(deltas))
			log.Printf(
				"The average pingpong was %v; max was %v",
				totalDuration/time.Duration(len(deltas)),
				maxTime,
			)
			return
		}

	}
}

type MoveToRight struct {
	Topology   topology.TwoBranched
	Vertex     *base.ClusterContext
	LeftFront  *base.ClusterContext
	LeftBack   *base.ClusterContext
	RightFront *base.ClusterContext
	RightBack  *base.ClusterContext

	frame2.DefaultRunDealer
}

// TODO: can this be made more generic, instead?
func (m *MoveToRight) Execute() error {

	log.Printf("LF: %+v\nLB: %+v\nRF: %+v\nRB: %+v\nVX: %+v\n", m.LeftFront, m.LeftBack, m.RightFront, m.RightBack, m.Vertex)
	validateHW := deploy.HelloWorldValidate{
		Namespace: m.Vertex,
	}
	validateOpts := frame2.RetryOptions{
		Ignore:     5,
		Ensure:     5,
		Timeout:    time.Minute * 30,
		KeepTrying: true,
	}

	p := frame2.Phase{
		Runner: m.Runner,
		Doc:    "Move Hello World from left to right",
		MainSteps: []frame2.Step{
			{
				Doc: "Move frontend from left to right",
				Modify: &composite.Migrate{
					From:       m.LeftFront,
					To:         m.RightFront,
					LinkTo:     []*base.ClusterContext{},
					LinkFrom:   []*base.ClusterContext{m.LeftBack, m.Vertex},
					UnlinkFrom: []*base.ClusterContext{m.Vertex},
					DeploySteps: []frame2.Step{
						{
							Doc: "Deploy new HelloWorld Frontend",
							Modify: &deploy.HelloWorldFrontend{
								Target:        m.RightFront,
								SkupperExpose: true,
							},
							Validator:      &validateHW,
							ValidatorRetry: validateOpts,
						},
					},
					UndeploySteps: []frame2.Step{
						{
							Doc: "Remove the application from the old frontend namespace",
							Modify: &execute.K8SUndeploy{
								Name:      "hello-world-frontend",
								Namespace: m.LeftFront,
								Wait:      2 * time.Minute,
							},
							Validator:      &validateHW,
							ValidatorRetry: validateOpts,
						},
					},
				},
			}, {
				Doc: "Move backend from left to right",
				Modify: &composite.Migrate{
					From:     m.LeftBack,
					To:       m.RightBack,
					LinkTo:   []*base.ClusterContext{m.RightFront},
					LinkFrom: []*base.ClusterContext{},
					DeploySteps: []frame2.Step{
						{
							Doc: "Deploy new HelloWorld Backend",
							Modify: &deploy.HelloWorldBackend{
								Target:        m.RightBack,
								SkupperExpose: true,
							},
							Validator:      &validateHW,
							ValidatorRetry: validateOpts,
						},
					},
					UndeploySteps: []frame2.Step{
						{
							Doc: "Remove the application from the old backend namespace",
							Modify: &execute.K8SUndeploy{
								Name:      "hello-world-backend",
								Namespace: m.LeftBack,
								Wait:      2 * time.Minute,
							},
							Validator:      &validateHW,
							ValidatorRetry: validateOpts,
						},
					},
				},
			},
		},
	}

	return p.Run()
}

type MoveToLeft struct {
	Topology   topology.TwoBranched
	Vertex     *base.ClusterContext
	LeftFront  *base.ClusterContext
	LeftBack   *base.ClusterContext
	RightFront *base.ClusterContext
	RightBack  *base.ClusterContext

	frame2.DefaultRunDealer
}

// TODO: can this be made more generic, instead?
func (m *MoveToLeft) Execute() error {

	log.Printf("LF: %+v\nLB: %+v\nRF: %+v\nRB: %+v\nVX: %+v\n", m.LeftFront, m.LeftBack, m.RightFront, m.RightBack, m.Vertex)
	validateHW := deploy.HelloWorldValidate{
		Namespace: m.Vertex,
	}
	validateOpts := frame2.RetryOptions{
		Ignore:     5,
		Ensure:     5,
		Timeout:    time.Minute * 30,
		KeepTrying: true,
	}

	p := frame2.Phase{
		Runner: m.Runner,
		Doc:    "Move Hello World from right to left",
		MainSteps: []frame2.Step{
			{
				Doc: "Move frontend from right to left",
				Modify: &composite.Migrate{
					From:       m.RightFront,
					To:         m.LeftFront,
					LinkTo:     []*base.ClusterContext{},
					LinkFrom:   []*base.ClusterContext{m.RightBack, m.Vertex},
					UnlinkFrom: []*base.ClusterContext{m.Vertex},
					DeploySteps: []frame2.Step{
						{
							Doc: "Deploy new HelloWorld Frontend",
							Modify: &deploy.HelloWorldFrontend{
								Target:        m.LeftFront,
								SkupperExpose: true,
							},
							Validator:      &validateHW,
							ValidatorRetry: validateOpts,
						},
					},
					UndeploySteps: []frame2.Step{
						{
							Doc: "Remove the application from the old frontend namespace",
							Modify: &execute.K8SUndeploy{
								Name:      "hello-world-frontend",
								Namespace: m.RightFront,
								Wait:      2 * time.Minute,
							},
							Validator:      &validateHW,
							ValidatorRetry: validateOpts,
						},
					},
				},
			}, {
				Doc: "Move backend from right to left",
				Modify: &composite.Migrate{
					From:     m.RightBack,
					To:       m.LeftBack,
					LinkTo:   []*base.ClusterContext{m.LeftFront},
					LinkFrom: []*base.ClusterContext{},
					DeploySteps: []frame2.Step{
						{
							Doc: "Deploy new HelloWorld Backend",
							Modify: &deploy.HelloWorldBackend{
								Target:        m.LeftBack,
								SkupperExpose: true,
							},
							Validator:      &validateHW,
							ValidatorRetry: validateOpts,
						},
					},
					UndeploySteps: []frame2.Step{
						{
							Doc: "Remove the application from the old backend namespace",
							Modify: &execute.K8SUndeploy{
								Name:      "hello-world-backend",
								Namespace: m.RightBack,
								Wait:      2 * time.Minute,
							},
							Validator:      &validateHW,
							ValidatorRetry: validateOpts,
						},
					},
				},
			},
		},
	}

	return p.Run()
}

// TestMain initializes flag parsing
func TestMain(m *testing.M) {
	base.ParseFlags()
	os.Exit(m.Run())
}
