package handlers

import "testing"

func TestValidHours(t *testing.T) {
	tests := []struct {
		name  string
		hours []hoursRequest
		want  bool
	}{
		{"vacío", nil, true},
		{"partido", []hoursRequest{{1, 600, 840}, {1, 960, 1230}}, true},
		{"solapado", []hoursRequest{{1, 600, 900}, {1, 840, 1000}}, false},
		{"invertido", []hoursRequest{{1, 900, 600}}, false},
		{"días distintos", []hoursRequest{{1, 600, 900}, {2, 600, 900}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := validHours(tc.hours); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSafeURLAndSnake(t *testing.T) {
	if safeURL("javascript:alert(1)") || !safeURL("https://x.y/a.png") || !safeURL("") {
		t.Fatal("safeURL")
	}
	if toSnake("DurationMin") != "duration_min" {
		t.Fatal(toSnake("DurationMin"))
	}
}
