package lint

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestCodexSkipsRequestsAnotherRunFinished(t *testing.T) {
	var (
		mutex sync.Mutex
		sent  []string
	)

	cli := codexCLIForTest(t, func(prompt string) (any, error) {
		_, encoded, _ := strings.Cut(prompt, codeReviewInputMarker)

		var inputs []codeReviewCodexInput
		if err := json.Unmarshal([]byte(encoded), &inputs); err != nil {
			return nil, err
		}

		records := make([]codeReviewRecord, len(inputs))

		mutex.Lock()
		for index, input := range inputs {
			sent = append(sent, input.Source)
			records[index] = codeReviewRecord{
				ID:    input.ID,
				Items: []codeReviewVerdict{{ID: "i1", Verdict: codeReviewKeep}},
			}
		}
		mutex.Unlock()

		return codeReviewCodexResponse{Declarations: records}, nil
	})

	request := func(id, source string) codeReviewRequest {
		return codeReviewRequest{
			ID:       strings.Repeat(id, 64),
			Location: "sample.go:1",
			Source:   source,
			Items:    []codeReviewItem{{ID: "i1", Kind: codeReviewCommentItem, Lines: "1"}},
		}
	}
	finished := request("a", "1| // finished elsewhere\n")
	pending := request("b", "1| // still pending\n")

	accepted := 0

	err := cli.review(
		t.Context(),
		[]codeReviewRequest{finished, pending},
		func(request codeReviewRequest) bool { return request.ID == finished.ID },
		func(records []codeReviewRecord) error {
			accepted += len(records)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(sent) != 1 || sent[0] != pending.Source || accepted != 1 {
		t.Fatalf("sent %q, accepted %d; want only the pending request", sent, accepted)
	}
}
