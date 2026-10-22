package detect

import "testing"

func TestCentre(t *testing.T) {
	tests := []struct {
		name       string
		w, h, size int
		want       Window
		wantErr    bool
	}{
		{name: "even margin", w: 4096, h: 4096, size: 128, want: Window{X: 1984, Y: 1984, Size: 128}},
		{name: "odd margin favours origin", w: 101, h: 101, size: 10, want: Window{X: 45, Y: 45, Size: 10}},
		{name: "non square image", w: 200, h: 100, size: 50, want: Window{X: 75, Y: 25, Size: 50}},
		{name: "exact fit", w: 128, h: 128, size: 128, want: Window{X: 0, Y: 0, Size: 128}},
		{name: "too wide", w: 100, h: 4096, size: 128, wantErr: true},
		{name: "too short", w: 4096, h: 100, size: 128, wantErr: true},
		{name: "zero size", w: 4096, h: 4096, size: 0, wantErr: true},
		{name: "negative size", w: 4096, h: 4096, size: -8, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Centre(tt.w, tt.h, tt.size)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Centre(%d, %d, %d) = %+v, want error", tt.w, tt.h, tt.size, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Centre: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Centre = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAt(t *testing.T) {
	tests := []struct {
		cx, cy, size int
		want         Window
	}{
		{2048, 2048, 128, Window{X: 1984, Y: 1984, Size: 128}},
		{2454, 1990, 128, Window{X: 2390, Y: 1926, Size: 128}},
		{64, 64, 128, Window{X: 0, Y: 0, Size: 128}},
		{100, 100, 5, Window{X: 98, Y: 98, Size: 5}},
	}

	for _, tt := range tests {
		if got := At(tt.cx, tt.cy, tt.size); got != tt.want {
			t.Errorf("At(%d, %d, %d) = %+v, want %+v", tt.cx, tt.cy, tt.size, got, tt.want)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		win     Window
		w, h    int
		wantErr bool
	}{
		{name: "inside", win: Window{X: 1, Y: 1, Size: 2}, w: 4, h: 4},
		{name: "flush bottom right", win: Window{X: 2, Y: 2, Size: 2}, w: 4, h: 4},
		{name: "exact fit", win: Window{X: 0, Y: 0, Size: 4}, w: 4, h: 4},
		{name: "negative origin", win: Window{X: -1, Y: 0, Size: 2}, w: 4, h: 4, wantErr: true},
		{name: "past right edge", win: Window{X: 3, Y: 0, Size: 2}, w: 4, h: 4, wantErr: true},
		{name: "past bottom edge", win: Window{X: 0, Y: 3, Size: 2}, w: 4, h: 4, wantErr: true},
		{name: "zero size", win: Window{X: 0, Y: 0, Size: 0}, w: 4, h: 4, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.win.Validate(tt.w, tt.h)
			if tt.wantErr && err == nil {
				t.Fatalf("Validate(%d, %d) = nil, want error", tt.w, tt.h)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}
