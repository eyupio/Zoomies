package provider

import (
	"strings"
	"testing"
)

// Both wire protocols stream server-sent events, and the differences between
// how servers write them (comments as keep-alives, data split over lines, a
// last event with no blank line after it) are the reader's to absorb once.
func TestReadEventsJoinsMultiLineDataAndSkipsComments(t *testing.T) {
	in := ": keep-alive\n\nevent: delta\ndata: {\"a\":\n\ndata: 1}\n\n: ping\ndata: second\n\n"
	var got []ServerEvent
	for e, err := range ReadEvents(strings.NewReader(in)) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	want := []ServerEvent{{Name: "delta", Data: "{\"a\":"}, {Name: "", Data: "1}"}, {Name: "", Data: "second"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadEventsJoinsDataLinesWithinOneEvent(t *testing.T) {
	in := "data: line one\ndata: line two\n\n"
	for e, err := range ReadEvents(strings.NewReader(in)) {
		if err != nil {
			t.Fatal(err)
		}
		if e.Data != "line one\nline two" {
			t.Errorf("data %q", e.Data)
		}
	}
}

func TestReadEventsStopsAtEOFWithTheUnterminatedEvent(t *testing.T) {
	in := "data: first\n\ndata: last without a blank line"
	var datas []string
	for e, err := range ReadEvents(strings.NewReader(in)) {
		if err != nil {
			t.Fatal(err)
		}
		datas = append(datas, e.Data)
	}
	if len(datas) != 2 || datas[1] != "last without a blank line" {
		t.Errorf("got %q", datas)
	}
}
