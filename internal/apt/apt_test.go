package apt

import (
	"context"
	"os"
	"reflect"
	"slices"
	"testing"
)

func fixtures(t *testing.T, full, plain string) Runner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		if !slices.Contains(args, "-s") {
			t.Fatalf("apt-get ran without -s: %v", args)
		}
		if args[len(args)-1] == "dist-upgrade" {
			return os.ReadFile(full)
		}
		return os.ReadFile(plain)
	}
}

func TestPendingListsUpdatesOfInstalledPackages(t *testing.T) {
	updates, err := Pending(context.Background(), fixtures(t, "testdata/dist-upgrade.txt", "testdata/upgrade.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Update{
		{Name: "libc6", From: "2.36-9+deb12u7", To: "2.36-9+deb12u8",
			Origins: []string{"Debian:12.7/stable"}},
		{Name: "linux-image-amd64", From: "6.1.106-3", To: "6.1.112-1",
			Origins: []string{"Debian-Security:12/stable-security"}, Security: true, HeldBack: true},
		{Name: "openssl", From: "3.0.14-1~deb12u1", To: "3.0.15-1~deb12u1",
			Origins: []string{"Debian:12.7/stable", "Debian-Security:12/stable-security"}, Security: true},
	}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("got  %+v\nwant %+v", updates, want)
	}
}

func TestUbuntuSecurityAndESMArchivesAreSecurity(t *testing.T) {
	b, _ := os.ReadFile("testdata/ubuntu-upgrade.txt")
	updates := parse(string(b))
	got := map[string]bool{}
	for _, u := range updates {
		got[u.Name] = u.Security
	}
	want := map[string]bool{"libssl3": true, "tzdata": false, "libc6:i386": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNothingPendingIsAnEmptyList(t *testing.T) {
	updates := parse("Reading package lists...\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n")
	if updates == nil || len(updates) != 0 {
		t.Fatalf("got %#v", updates)
	}
}
