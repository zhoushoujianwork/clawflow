package resourcelease

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTwoRepositoriesAndIndependentReview(t *testing.T) {
	root := t.TempDir()
	start := make(chan struct{})
	releaseWinner := make(chan struct{})
	results := make(chan error, 2)
	var active, maximum atomic.Int32
	var wg sync.WaitGroup
	for _, repo := range []string{"owner/a", "owner/b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			release, err := Acquire(root, []string{"desktop"}, Holder{Repository: repo, Task: "#1", Phase: "implement", Candidate: "candidate", Attempt: repo})
			if err != nil {
				results <- err
				return
			}
			n := active.Add(1)
			maximum.Store(n)
			results <- nil
			<-releaseWinner
			active.Add(-1)
			release()
		}()
	}
	close(start)
	var blocked *Blocked
	winners, losers := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
		} else if errors.As(err, &blocked) && blocked.Holder.Task == "#1" && blocked.Holder.Repository != "" {
			losers++
		} else {
			t.Errorf("unexpected result: %v", err)
		}
	}
	close(releaseWinner)
	wg.Wait()
	if winners != 1 || losers != 1 || maximum.Load() != 1 {
		t.Fatalf("winners=%d blocked=%d maximum=%d", winners, losers, maximum.Load())
	}
	release, err := Acquire(root, []string{"desktop"}, Holder{Repository: "owner/a", Task: "#1", Phase: "acceptance", Candidate: "candidate", Attempt: "fresh-review"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = Acquire(root, []string{"desktop"}, Holder{Repository: "owner/b", Task: "#2"})
	if !errors.As(err, &blocked) || blocked.Holder.Phase != "acceptance" || blocked.Holder.Candidate != "candidate" || blocked.Holder.Attempt != "fresh-review" {
		t.Fatalf("review did not reacquire: %v", err)
	}
}

func TestPartialAcquisitionRollsBack(t *testing.T) {
	root := t.TempDir()
	release, err := Acquire(root, []string{"z"}, Holder{Task: "holder"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Acquire(root, []string{"a", "z"}, Holder{Task: "contender"}); err == nil {
		t.Fatal("busy resource acquired")
	}
	releaseA, err := Acquire(root, []string{"a", "a"}, Holder{Task: "other"})
	if err != nil {
		t.Fatal(err)
	}
	releaseA()
	releaseA()
	releaseB, err := Acquire(root, []string{"a"}, Holder{Task: "successor"})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()
	releaseA()
	_, err = Acquire(root, []string{"a"}, Holder{Task: "stale-release-contender"})
	var busy *Blocked
	if !errors.As(err, &busy) || busy.Holder.Task != "successor" {
		t.Fatalf("stale release unlocked successor: %v", err)
	}
}

func TestDeadHolderExpires(t *testing.T) {
	if root := os.Getenv("CLAWFLOW_RESOURCE_LEASE_HELPER"); root != "" {
		release, err := Acquire(root, []string{"desktop"}, Holder{Repository: "owner/a", Task: "crashed", Phase: "implement"})
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		fmt.Println("acquired")
		// Stay alive until the parent kills this process without releasing.
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		os.Exit(0)
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDeadHolderExpires$")
	cmd.Env = append(os.Environ(), "CLAWFLOW_RESOURCE_LEASE_HELPER="+root)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "acquired\n" {
		t.Fatalf("helper: %q", line)
	}
	_, err = Acquire(root, []string{"desktop"}, Holder{Task: "contender"})
	var blocked *Blocked
	if !errors.As(err, &blocked) || blocked.Holder.Task != "crashed" {
		t.Fatalf("live holder preempted: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	release, err := Acquire(root, []string{"desktop"}, Holder{Task: "recovered", Phase: "acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	release()
}
