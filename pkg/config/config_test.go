package config

import (
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"

	"github.com/frinknet/caddy-savefile/pkg/paths"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "1024", want: 1024},
		{in: "1b", want: 1},
		{in: "1KB", want: 1 << 10},
		{in: "50MB", want: 50 << 20},
		{in: "1GB", want: 1 << 30},
		{in: "1.5MB", want: 1_572_864},
		{in: "10 MB", want: 10 << 20},
		{in: "", wantErr: true},
		{in: "MB", wantErr: true},
		{in: "10XB", wantErr: true},
		{in: "0MB", wantErr: true},
		{in: "-5MB", wantErr: true},
	}

	for _, test := range tests {
		got, err := ParseSize(test.in)
		if test.wantErr {
			if err == nil {
				t.Errorf("ParseSize(%q) = %d, want error", test.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSize(%q): %v", test.in, err)
			continue
		}
		if got != test.want {
			t.Errorf("ParseSize(%q) = %d, want %d", test.in, got, test.want)
		}
	}
}

func TestParseGlobalOptionVerifyMime(t *testing.T) {
	original := Global()
	t.Cleanup(func() { Store(original) })

	d := caddyfile.NewTestDispenser("savefile {\n\tverify_mime strict\n\tmax_parts 7\n}")
	if _, err := parseGlobalOption(d, nil); err != nil {
		t.Fatalf("parseGlobalOption: %v", err)
	}

	got := Global()
	if got.VerifyMime != paths.VerifyMimeStrict {
		t.Errorf("VerifyMime = %q, want strict", got.VerifyMime)
	}
	if got.MaxParts != 7 {
		t.Errorf("MaxParts = %d, want 7", got.MaxParts)
	}

	bad := caddyfile.NewTestDispenser("savefile {\n\tverify_mime sometimes\n}")
	if _, err := parseGlobalOption(bad, nil); err == nil {
		t.Error("an unknown verify_mime value was accepted")
	}
}

func TestDefaultsAndStore(t *testing.T) {
	original := Global()
	t.Cleanup(func() { Store(original) })

	if original.RootDir != DefaultRootDir || original.MaxSize != DefaultMaxSize {
		t.Fatalf("unexpected defaults: %+v", original)
	}

	custom := Config{RootDir: "/tmp/root", MaxSize: 1234, TempDir: "/tmp/scratch"}
	Store(custom)
	if got := Global(); got != custom {
		t.Fatalf("Global() = %+v, want %+v", got, custom)
	}

	if got := MaxSizeOr(0); got != 1234 {
		t.Errorf("MaxSizeOr(0) = %d, want 1234", got)
	}
	if got := MaxSizeOr(7); got != 7 {
		t.Errorf("MaxSizeOr(7) = %d, want 7", got)
	}
}
