package main

import (
	"testing"
	"time"
)

func TestParseScenario(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  scenario
	}{
		{name: "指定なし", query: "", want: scenario{status: 200}},
		{name: "status のみ", query: "status=503", want: scenario{status: 503}},
		{name: "全部", query: "fail=2&status=503&delay=2s", want: scenario{status: 503, delay: 2 * time.Second, fail: 2}},
		{name: "status の下限", query: "status=200", want: scenario{status: 200}},
		{name: "status の上限", query: "status=599", want: scenario{status: 599}},
		{name: "delay の上限", query: "delay=60s", want: scenario{status: 200, delay: 60 * time.Second}},
		{name: "delay が 0", query: "delay=0s", want: scenario{status: 200}},
		{name: "fail の上限", query: "fail=8&status=500", want: scenario{status: 500, fail: 8}},
		{name: "fail と 3xx", query: "fail=1&status=302", want: scenario{status: 302, fail: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, bad := parseScenario(tt.query)
			if bad != "" {
				t.Fatalf("reason = %q", bad)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseScenarioRejects(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  reason
	}{
		{name: "壊れたエンコード", query: "status=%zz", want: reasonMalformedQuery},
		{name: "知らない名前", query: "stauts=503", want: reasonUnknownParameter},
		{name: "重複", query: "status=503&status=500", want: reasonDuplicateParameter},
		{name: "空の値", query: "status=", want: reasonEmptyValue},
		{name: "値のない名前", query: "status", want: reasonEmptyValue},
		{name: "status が数でない", query: "status=abc", want: reasonInvalidStatus},
		{name: "status が 1xx", query: "status=199", want: reasonInvalidStatus},
		{name: "status が範囲外", query: "status=600", want: reasonInvalidStatus},
		{name: "delay が読めない", query: "delay=2", want: reasonInvalidDelay},
		{name: "delay が負", query: "delay=-1s", want: reasonInvalidDelay},
		{name: "delay が上限超え", query: "delay=61s", want: reasonInvalidDelay},
		{name: "fail が 0", query: "fail=0&status=503", want: reasonInvalidFail},
		{name: "fail が負", query: "fail=-1&status=503", want: reasonInvalidFail},
		{name: "fail が上限超え", query: "fail=9&status=503", want: reasonInvalidFail},
		{name: "fail が数でない", query: "fail=x&status=503", want: reasonInvalidFail},
		{name: "fail と既定の status", query: "fail=2", want: reasonFailRequiresErrorStatus},
		{name: "fail と 2xx", query: "fail=2&status=204", want: reasonFailRequiresErrorStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := parseScenario(tt.query)
			if got != tt.want {
				t.Errorf("reason = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScenarioStatusFor(t *testing.T) {
	failing := scenario{status: 503, fail: 2}
	for attempt, want := range map[int]int{1: 503, 2: 503, 3: 200, 4: 200} {
		if got := failing.statusFor(attempt); got != want {
			t.Errorf("fail=2: attempt %d = %d, want %d", attempt, got, want)
		}
	}

	always := scenario{status: 500}
	for _, attempt := range []int{1, 9, 100} {
		if got := always.statusFor(attempt); got != 500 {
			t.Errorf("fail なし: attempt %d = %d, want 500", attempt, got)
		}
	}
}
