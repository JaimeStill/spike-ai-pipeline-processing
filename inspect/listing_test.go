package inspect_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// checksOf returns the checks of r's flags, in order.
func checksOf(r inspect.Report) []inspect.Check {
	var checks []inspect.Check
	for _, f := range r.Flags {
		checks = append(checks, f.Check)
	}
	return checks
}

// TestCheckListing: a 7z listing raises a check for each rule tar's headers are held to, by the
// same name checks, each flag's evidence naming its entry, and an encrypted entry is active; a
// cabinet's listing raises its names and its bomb; a zstd stream's raises its bomb alone, by its
// declared size; and an entry with no declared size is no bomb.
func TestCheckListing(t *testing.T) {
	unsafe := []inspect.Listed{
		{Name: "notes.txt", Type: inspect.FileEntry, Declared: 18, Sized: true},
		{Name: "docs/../../evil.txt", Type: inspect.FileEntry},
		{Name: `C:\evil.txt`, Type: inspect.FileEntry},
		{Name: "passwd", Type: inspect.SymlinkEntry, Link: "/etc/passwd"},
		{Name: "shadow", Type: inspect.HardlinkEntry, Link: "/etc/shadow"},
		{Name: "pipe", Type: inspect.DeviceEntry},
		{Name: "secret.txt", Type: inspect.FileEntry, Encrypted: true},
		{Name: "zeros.bin", Type: inspect.FileEntry, Declared: 4 << 30, Sized: true},
	}
	for _, c := range []struct {
		kind route.Kind
		want []inspect.Check
	}{
		{route.SevenZip, []inspect.Check{
			inspect.SevenZipAbsolutePath, inspect.SevenZipBomb, inspect.SevenZipDevice, inspect.SevenZipEncrypted,
			inspect.SevenZipHardlink, inspect.SevenZipSymlink, inspect.SevenZipTraversal,
		}},
		{route.MSCF, []inspect.Check{inspect.CabAbsolutePath, inspect.CabBomb, inspect.CabTraversal}},
		{route.Zstd, []inspect.Check{inspect.ZstdBomb}},
	} {
		rep, err := inspect.CheckListing(c.kind, 1024, unsafe, inspect.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if got := checksOf(rep); !slices.Equal(got, c.want) || rep.Verdict != inspect.Quarantine || rep.Kind != c.kind {
			t.Errorf("%s: %v, %s, want %v, quarantine", c.kind, got, rep.Verdict, c.want)
		}
		for _, f := range rep.Flags {
			if f.Class != inspect.ClassOf(f.Check) || !strings.HasPrefix(f.Evidence, "entry ") {
				t.Errorf("%s: %s: class %s, evidence %q", c.kind, f.Check, f.Class, f.Evidence)
			}
		}
	}
	rep, err := inspect.CheckListing(route.SevenZip, 1024, unsafe[1:2], inspect.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Flags) != 1 || rep.Flags[0].Evidence != `entry "docs/../../evil.txt"` {
		t.Errorf("traversal's flags %v", rep.Flags)
	}
	rep, err = inspect.CheckListing(route.SevenZip, 1024, unsafe[7:], inspect.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if want := `entry "zeros.bin": 4294967296 bytes declared in a 1024-byte archive, 4194304:1`; len(rep.Flags) != 1 || rep.Flags[0].Evidence != want {
		t.Errorf("bomb's flags %v, want evidence %q", rep.Flags, want)
	}

	// An encrypted entry alone is active, so the archive proceeds.
	rep, err = inspect.CheckListing(route.SevenZip, 1024, unsafe[6:7], inspect.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := checksOf(rep); !slices.Equal(got, []inspect.Check{inspect.SevenZipEncrypted}) || rep.Verdict != inspect.Proceed {
		t.Errorf("encrypted: %v, %s", got, rep.Verdict)
	}
	// An entry with no declared size is no bomb, as a zstd stream's whose header declares none.
	rep, err = inspect.CheckListing(route.Zstd, 10, []inspect.Listed{{Name: "zeros", Type: inspect.FileEntry}}, inspect.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Flags) != 0 || rep.Verdict != inspect.Proceed {
		t.Errorf("unsized zstd: %v, %s", rep.Flags, rep.Verdict)
	}
}

// TestCheckListingOptions: a policy lets a listing's unsafe flags proceed, lowered thresholds make
// a smaller entry a bomb, invalid options are refused as Inspect refuses them, and a kind with no
// listing checks is refused.
func TestCheckListingOptions(t *testing.T) {
	traversal := []inspect.Listed{{Name: "../evil.txt", Type: inspect.FileEntry}}
	rep, err := inspect.CheckListing(route.SevenZip, 100, traversal, inspect.Options{
		Policy: map[inspect.Check]inspect.Verdict{inspect.SevenZipTraversal: inspect.Proceed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Has(inspect.SevenZipTraversal) || rep.Verdict != inspect.Proceed {
		t.Errorf("traversal under a permissive policy: %v, %s", rep.Flags, rep.Verdict)
	}
	eight := []inspect.Listed{{Name: "zeros.bin", Type: inspect.FileEntry, Declared: 8 << 20, Sized: true}}
	if rep, err := inspect.CheckListing(route.MSCF, 14662, eight, inspect.Options{}); err != nil || len(rep.Flags) != 0 {
		t.Errorf("8 MiB under the defaults: %v, %v", rep.Flags, err)
	}
	if rep, err := inspect.CheckListing(route.MSCF, 14662, eight, inspect.Options{BombSize: 1 << 20, BombRatio: 100}); err != nil || !rep.Has(inspect.CabBomb) {
		t.Errorf("8 MiB past lowered thresholds: %v, %v", rep.Flags, err)
	}
	if _, err := inspect.CheckListing(route.SevenZip, 1, nil, inspect.Options{BombRatio: -1}); !errors.Is(err, inspect.ErrOptions) {
		t.Errorf("a negative ratio: %v, want ErrOptions", err)
	}
	if _, err := inspect.CheckListing(route.Tar, 1, nil, inspect.Options{}); err == nil {
		t.Error("a tar's listing was checked; Inspect reads a tar's headers itself")
	}
}
