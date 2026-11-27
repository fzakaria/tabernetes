package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fzakaria/tabernetes/internal/kube"
	"github.com/fzakaria/tabernetes/internal/tab"
)

// The residents' word in these tests, and a gibibyte.
const (
	testWord = "the-residents-word"
	gibibyte = int64(1) << 30
)

// testAgent returns an agent over a stand-in for an API server, with
// the settings a test gives on top of ones that turn nobody away.
func testAgent(t *testing.T, change func(*Config)) *Agent {
	t.Helper()
	return testAgentOver(t, fake.NewSimpleClientset(), change)
}

// testAgentOver is testAgent over a stand-in the test keeps, to look at
// what the agent wrote to the API server.
func testAgentOver(t *testing.T, client kubernetes.Interface, change func(*Config)) *Agent {
	t.Helper()
	config := Config{
		Cluster:            kube.Over(client),
		MaxNodes:           100,
		MaxNodesPerAddress: 100,
		ResidentWord:       testWord,
		Zone:               "America/Los_Angeles",
		StateDir:           t.TempDir(),
		Log:                log.New(io.Discard, "", 0),
	}
	if change != nil {
		change(&config)
	}
	agent := New(config)
	agent.ctx = context.Background()
	return agent
}

// writeVitals makes a stand-in for /proc that says the machine has 8 GiB
// of memory with so much of it available, and that its CPUs have had so
// many ticks of time, so many of them idle.
func writeVitals(t *testing.T, root string, available int64, ticks, idle int) {
	t.Helper()
	files := map[string]string{
		"meminfo": fmt.Sprintf("MemTotal: %d kB\nMemAvailable: %d kB\n", 8*gibibyte/1024, available/1024),
		"stat":    fmt.Sprintf("cpu  %d 0 0 %d 0 0 0 0 0 0\n", ticks-idle, idle),
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestJoinTurnedAwayWhenMemoryIsShort checks that a tab is taken while
// the machine has more memory to give than the agent keeps back, and
// turned away as full once the machine has less: by reading a stand-in
// for /proc before each join.
func TestJoinTurnedAwayWhenMemoryIsShort(t *testing.T) {
	proc := t.TempDir()
	agent := testAgent(t, func(config *Config) {
		config.Proc = proc
		config.ReserveMemory = gibibyte
	})

	writeVitals(t, proc, 2*gibibyte, 100, 100)
	agent.readVitals()
	if _, err := agent.Join(context.Background(), tab.Hello{Model: "Pixel 9"}, "198.51.100.1"); err != nil {
		t.Fatalf("with memory to spare: %v", err)
	}

	writeVitals(t, proc, gibibyte/2, 200, 200)
	agent.readVitals()
	if _, err := agent.Join(context.Background(), tab.Hello{Model: "Pixel 9"}, "198.51.100.2"); !errors.Is(err, ErrFull) {
		t.Errorf("with memory short: %v", err)
	}
}

// TestJoinAskedToWaitWhenBusy checks that a tab is asked to wait, and
// not told the cluster is full, while the machine's CPUs have been busy
// more than the agent allows: by reading a stand-in for /proc twice,
// with every tick between the readings spent busy.
func TestJoinAskedToWaitWhenBusy(t *testing.T) {
	proc := t.TempDir()
	agent := testAgent(t, func(config *Config) {
		config.Proc = proc
		config.BusyAbove = 0.2
	})

	writeVitals(t, proc, 4*gibibyte, 1000, 1000)
	agent.readVitals()
	writeVitals(t, proc, 4*gibibyte, 2000, 1000)
	agent.readVitals()
	if _, err := agent.Join(context.Background(), tab.Hello{Model: "Pixel 9"}, "198.51.100.1"); !errors.Is(err, ErrBusy) {
		t.Errorf("with the machine busy: %v", err)
	}
}

// TestResident checks what the residents' word does for a tab that
// says it: the node is named a resident, placed where the machine is,
// labelled, and not counted against its address; and that a tab with
// the wrong word is a visitor like any other.
func TestResident(t *testing.T) {
	client := fake.NewSimpleClientset()
	agent := testAgentOver(t, client, func(config *Config) { config.MaxNodesPerAddress = 1 })
	ctx := context.Background()

	// Two residents from one address, where one visitor is the most.
	for range 2 {
		node, err := agent.Join(ctx, tab.Hello{Platform: "Linux", Zone: "UTC", Resident: testWord}, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(node.Name, "resident-") || node.Origin != tab.Resident {
			t.Errorf("a resident is named %q, origin %v", node.Name, node.Origin)
		}
		if node.Hello.Zone != "America/Los_Angeles" {
			t.Errorf("a resident is placed in %q", node.Hello.Zone)
		}
		made, err := client.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil || made.Labels[kube.LabelResident] != "true" {
			t.Errorf("a resident's Node: %v, labels %v", err, made.GetLabels())
		}
	}

	// The wrong word makes a visitor, who is held to the limit.
	visitor, err := agent.Join(ctx, tab.Hello{Platform: "Linux", Resident: "a guess"}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if visitor.Origin != tab.Visitor || strings.HasPrefix(visitor.Name, "resident-") {
		t.Errorf("a tab with the wrong word is %q, origin %v", visitor.Name, visitor.Origin)
	}
	if _, err := agent.Join(ctx, tab.Hello{Platform: "Linux"}, "127.0.0.1"); !errors.Is(err, ErrTooManyHere) {
		t.Errorf("a second visitor from the address: %v", err)
	}
}

// TestVisitorTakesAResidentsPlace checks that a full cluster still
// takes a visitor while a resident holds a place, by giving the
// resident up, and that neither a resident nor a visitor is taken once
// only visitors are left: with room for two nodes and two residents in
// them.
func TestVisitorTakesAResidentsPlace(t *testing.T) {
	agent := testAgent(t, func(config *Config) { config.MaxNodes = 2 })
	ctx := context.Background()

	var residents []*Node
	for range 2 {
		node, err := agent.Join(ctx, tab.Hello{Resident: testWord}, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		residents = append(residents, node)
	}
	if _, err := agent.Join(ctx, tab.Hello{Resident: testWord}, "127.0.0.1"); !errors.Is(err, ErrFull) {
		t.Errorf("a third resident: %v", err)
	}

	// Each visitor is taken, and a resident is given up for each. The
	// agent's server is what answers a resident's loss with its
	// leaving; here the test does.
	for index := range 2 {
		if _, err := agent.Join(ctx, tab.Hello{Model: "Pixel 9"}, fmt.Sprintf("198.51.100.%d", index)); err != nil {
			t.Fatalf("visitor %d: %v", index, err)
		}
		lost := 0
		for _, resident := range residents {
			select {
			case <-resident.Lost():
				agent.Leave(resident)
				lost++
			default:
			}
		}
		if lost != index+1 {
			t.Errorf("after visitor %d, %d residents were given up", index, lost)
		}
	}
	if _, err := agent.Join(ctx, tab.Hello{Model: "Pixel 9"}, "198.51.100.9"); !errors.Is(err, ErrFull) {
		t.Errorf("a visitor with no resident left to replace: %v", err)
	}
}

// TestMovesElsewhere checks which pods are counted as moved when their
// tab goes: a pod some controller will make again for another node is,
// and a DaemonSet's pod, which belongs to the one node, and a pod
// nobody owns are not.
func TestMovesElsewhere(t *testing.T) {
	controller := true
	owned := func(kind string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{{Kind: kind, Name: "owner", Controller: &controller}},
		}}
	}
	if !movesElsewhere(owned("ReplicaSet")) {
		t.Error("a ReplicaSet's pod does not move")
	}
	if movesElsewhere(owned("DaemonSet")) {
		t.Error("a DaemonSet's pod moves")
	}
	if movesElsewhere(&corev1.Pod{}) {
		t.Error("a pod nobody owns moves")
	}
}

// TestTurns checks that no more pods are at the machine's part of
// starting than the agent allows at once, that a turn given back lets
// the next pod in, and that a pod whose run ends while it waits stops
// waiting: with room for one.
func TestTurns(t *testing.T) {
	agent := testAgent(t, func(config *Config) { config.StartsAtOnce = 1 })

	first, err := agent.takeTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// A second pod waits, and gives up when its run is stopped.
	stopped, stop := context.WithCancel(context.Background())
	stop()
	if _, err := agent.takeTurn(stopped); err == nil {
		t.Error("a second pod got a turn while the first had the only one")
	}

	// Giving a turn back twice gives one turn back.
	first()
	first()
	second, err := agent.takeTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.takeTurn(stopped); err == nil {
		t.Error("a turn given back twice made room for two")
	}
	second()
}
