package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCalculator(t *testing.T) {
	tests := []struct {
		expr, want string
		invalid    bool
	}{
		{"23*17", "391", false}, {"(2+3)*4", "20", false}, {"-5 + 2.5", "-2.5", false}, {"1/0", "", true}, {"2**3", "", true}, {"system('x')", "", true}, {"(((((((((((((((((1)))))))))))))))))", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"expression": tt.expr})
			got, err := (Calculator{}).Execute(context.Background(), raw)
			if tt.invalid {
				if err == nil {
					t.Fatalf("expected error, got %q", got.ModelText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.ModelText != tt.want {
				t.Fatalf("got %q want %q", got.ModelText, tt.want)
			}
		})
	}
}
