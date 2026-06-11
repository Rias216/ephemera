package llm

import (
	"strings"
	"testing"
)

func TestScanSSE(t *testing.T) {
	input := "event: message\ndata: {\"value\":1}\n\ndata: [DONE]\n\n"
	var frames []string
	err := scanSSE(strings.NewReader(input), func(data string) error {
		frames = append(frames, data)
		if data == "[DONE]" {
			return errSSEDone
		}
		return nil
	})
	if err != errSSEDone {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 2 || frames[0] != `{"value":1}` || frames[1] != "[DONE]" {
		t.Fatalf("unexpected frames: %#v", frames)
	}
}
