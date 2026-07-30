package saltdriver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

type commandCall struct {
	name string
	args []string
}
type fakeRunner struct {
	calls   []commandCall
	outputs [][]byte
	errors  []error
}

func (f *fakeRunner) CombinedOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, commandCall{name, append([]string(nil), args...)})
	index := len(f.calls) - 1
	var output []byte
	if index < len(f.outputs) {
		output = f.outputs[index]
	}
	var err error
	if index < len(f.errors) {
		err = f.errors[index]
	}
	return output, err
}

func TestDecodeStreamingJSONAcceptsPerMinionObjects(t *testing.T) {
	body := []byte("{\"minion-a\":{\"ok\":true}}\n{\"minion-b\":{\"ok\":false}}\n")
	value, found, err := MinionResult(body, "minion-b")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	var decoded map[string]bool
	if json.Unmarshal(value, &decoded) != nil || decoded["ok"] {
		t.Fatalf("unexpected result %s", value)
	}
}

func TestAcceptedKeysUsesFixedCommandAndVersionGate(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		[]byte(`{"minions":["minion-b","minion-a","../bad"]}`),
		[]byte(`{"minion-a":"3008.1"}`),
		[]byte(`{"minion-b":"3007.8"}`),
	}}
	driver := New(runner)
	nodes, err := driver.AcceptedNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].MinionID != "minion-a" || !nodes[0].Online || nodes[1].Online {
		t.Fatalf("nodes=%+v", nodes)
	}
	want := commandCall{"salt-key", []string{"--out=json", "--list=acc"}}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("call=%+v want=%+v", runner.calls[0], want)
	}
}

func TestCallRejectsArbitraryFunctionBeforeExecution(t *testing.T) {
	runner := &fakeRunner{}
	driver := New(runner)
	_, err := driver.Call(context.Background(), "minion", "cmd.run", "rm", "-rf")
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrUnsupportedOperation {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unexpected command calls: %+v", runner.calls)
	}
}

func TestStageUsesChunkedFixedArgvAndCleanup(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{[]byte(`{"minion":true}`)}}
	driver := New(runner)
	driver.StagingRoot = t.TempDir()
	bundle, err := driver.Stage(context.Background(), "minion", map[string]any{"manifest": "safe"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle.LocalPath); err != nil {
		t.Fatal(err)
	}
	call := runner.calls[0]
	if call.name != "salt-cp" || len(call.args) != 6 || call.args[0] != "--chunked" || call.args[3] != "minion" {
		t.Fatalf("call=%+v", call)
	}
	driver.Cleanup(bundle)
	if _, err := os.Stat(bundle.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging file not removed: %v", err)
	}
}

func TestPollReturnsJobCacheMissing(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{[]byte(`{}`), []byte(`{}`)}}
	driver := New(runner)
	driver.PollTimeout = 0
	driver.now = func() time.Time { return time.Unix(100, 0) }
	_, err := driver.Poll(context.Background(), "20260730120000000000", "minion")
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrSaltJobMissing {
		t.Fatalf("err=%v calls=%+v", err, runner.calls)
	}
}

func TestPublishAssetsIsContentAddressed(t *testing.T) {
	driver := New(&fakeRunner{})
	driver.StateRoot = t.TempDir()
	first, err := driver.PublishAssets()
	if err != nil {
		t.Fatal(err)
	}
	second, err := driver.PublishAssets()
	if err != nil || first != second || len(first) != 64 {
		t.Fatalf("first=%q second=%q err=%v", first, second, err)
	}
	for _, relative := range []string{"_modules/toolhub.py", "_states/toolhub.py", "toolhub/init.sls"} {
		if _, err := os.Stat(filepath.Join(driver.StateRoot, relative)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublishAssetsIsSafeUnderConcurrency(t *testing.T) {
	driver := New(&fakeRunner{})
	driver.StateRoot = t.TempDir()
	const callers = 12
	hashes := make(chan string, callers)
	errorsFound := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			hash, err := driver.PublishAssets()
			hashes <- hash
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(hashes)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := ""
	for hash := range hashes {
		if want == "" {
			want = hash
		}
		if hash != want || len(hash) != 64 {
			t.Fatalf("concurrent asset hash=%q want=%q", hash, want)
		}
	}
}

func TestResolveManagedHomeUsesFixedUserInfo(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{[]byte(`{"minion":{"name":"runner","home":"/srv/users/runner"}}`)}}
	driver := New(runner)
	home, err := driver.ResolveManagedHome(context.Background(), "minion", "runner")
	if err != nil || home != "/srv/users/runner" {
		t.Fatalf("home=%q err=%v", home, err)
	}
	want := commandCall{"salt", []string{"--out=json", "--static", "--timeout=60", "--", "minion", "user.info", "runner"}}
	if !reflect.DeepEqual(runner.calls, []commandCall{want}) {
		t.Fatalf("calls=%+v want=%+v", runner.calls, want)
	}
}

func TestResolveManagedHomeRejectsUnsafeResult(t *testing.T) {
	for _, body := range []string{`{"minion":false}`, `{"minion":{"name":"runner","home":"/"}}`, `{"minion":{"name":"other","home":"/home/runner"}}`} {
		runner := &fakeRunner{outputs: [][]byte{[]byte(body)}}
		_, err := New(runner).ResolveManagedHome(context.Background(), "minion", "runner")
		var apiErr *bridgeprotocol.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrManagedUserMissing {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
}
