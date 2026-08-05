package adapters

import (
	"testing"
)

func TestExtractJSONPath(t *testing.T) {
	data := map[string]any{
		"is_available": true,
		"balance_infos": []any{
			map[string]any{
				"currency":           "CNY",
				"total_balance":      "110.00",
				"granted_balance":    "10.00",
				"topped_up_balance":  "100.00",
			},
			map[string]any{
				"currency":           "USD",
				"total_balance":      "50.00",
				"granted_balance":    "5.00",
				"topped_up_balance":  "45.00",
			},
		},
		"usage": map[string]any{
			"total_spent": 42.5,
		},
	}

	tests := []struct {
		name     string
		path     string
		want     any
		wantOk   bool
	}{
		{
			name:   "nested field in array",
			path:   ".balance_infos[0].total_balance",
			want:   "110.00",
			wantOk: true,
		},
		{
			name:   "currency from array",
			path:   ".balance_infos[0].currency",
			want:   "CNY",
			wantOk: true,
		},
		{
			name:   "filter: find by currency=CNY",
			path:   ".balance_infos[currency=CNY].total_balance",
			want:   "110.00",
			wantOk: true,
		},
		{
			name:   "filter: find by currency=USD",
			path:   ".balance_infos[currency=USD].total_balance",
			want:   "50.00",
			wantOk: true,
		},
		{
			name:   "filter: no match",
			path:   ".balance_infos[currency=EUR].total_balance",
			want:   nil,
			wantOk: false,
		},
		{
			name:   "float value",
			path:   ".usage.total_spent",
			want:   42.5,
			wantOk: true,
		},
		{
			name:   "top-level field",
			path:   ".is_available",
			want:   true,
			wantOk: true,
		},
		{
			name:   "missing field",
			path:   ".nonexistent.field",
			want:   nil,
			wantOk: false,
		},
		{
			name:   "out of bounds index",
			path:   ".balance_infos[5].currency",
			want:   nil,
			wantOk: false,
		},
		{
			name:   "empty path",
			path:   "",
			want:   nil,
			wantOk: false,
		},
		{
			name:   "just dot",
			path:   ".",
			want:   nil,
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractJSONPath(data, tt.path)
			if ok != tt.wantOk {
				t.Errorf("ok = %v, want %v", ok, tt.wantOk)
			}
			if ok && got != tt.want {
				t.Errorf("got %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestExtractJSONPathNumericConversion(t *testing.T) {
	// When the path resolves to a string that looks numeric (like "110.00"),
	// extractJSONPathFloat should parse it.
	data := map[string]any{
		"balance_infos": []any{
			map[string]any{
				"total_balance": "110.00",
				"currency":      "CNY",
			},
		},
	}

	t.Run("string to float", func(t *testing.T) {
		v, ok := extractJSONPath(data, ".balance_infos[0].total_balance")
		if !ok {
			t.Fatal("path not found")
		}
		s, ok := v.(string)
		if !ok {
			t.Fatalf("expected string, got %T", v)
		}
		f, err := parseNumeric(s)
		if err != nil {
			t.Fatalf("parseNumeric(%q): %v", s, err)
		}
		if f != 110.0 {
			t.Errorf("got %v, want 110.0", f)
		}
	})

	t.Run("string to float with parseNumeric", func(t *testing.T) {
		v, ok := extractJSONPath(data, ".balance_infos[0].total_balance")
		if !ok {
			t.Fatal("path not found")
		}
		got, ok := extractJSONPathFloat(data, ".balance_infos[0].total_balance")
		if !ok {
			t.Fatal("extractJSONPathFloat failed")
		}
		_ = v
		if got != 110.0 {
			t.Errorf("got %v, want 110.0", got)
		}
	})

	t.Run("missing path returns zero", func(t *testing.T) {
		_, ok := extractJSONPathFloat(data, ".nonexistent")
		if ok {
			t.Error("expected false for missing path")
		}
	})
}
