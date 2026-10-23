package dao

import (
	"sync"
	"testing"

	"backend/dal/model"
	"backend/internal/inputtestdb"
	"backend/pkg/human"
)

func inputHistory() []model.Message {
	calls := `[{"id":"q","type":"function","function":{"name":"request_input","arguments":"{\"question\":\"格式？\"}"}},{"id":"write","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"x\",\"content\":\"y\"}"}}]`
	return []model.Message{{Role: "user", MessageID: "user"}, {Role: "assistant", MessageID: "assistant", ToolCalls: &calls}}
}

func TestInputConcurrentAnswersAndRetry(t *testing.T) {
	ctx, store := inputtestdb.New(t, inputHistory()...)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := AnswerInput(ctx, "session", "assistant", "q", "answer", "PDF")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(store.Snapshot()) != 3 || store.Locks != 12 {
		t.Fatal("concurrent retries must take DB lock and insert exactly once")
	}
	if _, err := AnswerInput(ctx, "session", "assistant", "q", "answer", "Word"); err == nil {
		t.Fatal("conflicting answer must be rejected")
	}
	if _, err := AnswerInput(ctx, "session", "assistant", "write", "answer", "yes"); err == nil {
		t.Fatal("cannot answer a normal tool")
	}
}

func TestInputSkipStopAndMixedBatch(t *testing.T) {
	ctx, store := inputtestdb.New(t, inputHistory()...)
	if victim := RollbackDanglingTurn(ctx, "session"); victim != nil {
		t.Fatal("must not roll back an unanswered question")
	}
	if err := PrepareInputBatch(ctx, "session", "assistant"); err != nil {
		t.Fatal(err)
	}
	if err := PrepareInputBatch(ctx, "session", "assistant"); err != nil {
		t.Fatal(err)
	}
	if len(store.Snapshot()) != 3 {
		t.Fatal("sibling closure must be idempotent")
	}
	if _, err := AnswerInput(ctx, "session", "assistant", "q", "skip", ""); err != nil {
		t.Fatal(err)
	}
	if human.Active(store.Snapshot()) == nil {
		t.Fatal("skip should permit continuation")
	}
	if err := StopInput(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	n := len(store.Snapshot())
	if err := StopInput(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	if human.Active(store.Snapshot()) != nil || len(store.Snapshot()) != n {
		t.Fatal("stop must durably close the turn and be idempotent")
	}
}

func TestInputStopWinsOverAnswer(t *testing.T) {
	ctx, store := inputtestdb.New(t, inputHistory()...)
	if err := StopInput(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := AnswerInput(ctx, "session", "assistant", "q", "answer", "PDF"); err == nil {
		t.Fatal("stopped question must reject late answers")
	}
	if human.Active(store.Snapshot()) != nil {
		t.Fatal("stopped task cannot resume")
	}
}
