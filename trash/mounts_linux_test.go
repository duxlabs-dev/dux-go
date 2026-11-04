package trash

import (
	"slices"
	"testing"
)

func TestParseMountInfoSkipsAutofsPlaceholders(t *testing.T) {
	data := "" +
		"25 1 0:22 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw\n" +
		"60 25 0:40 / /mnt/TrueNAS rw,relatime shared:30 - autofs systemd-1 rw,fd=50,pgrp=1,timeout=0,minproto=5,maxproto=5,direct,pipe_ino=1\n" +
		"61 25 0:41 / /mnt/Windows rw,relatime shared:31 - autofs systemd-1 rw,fd=51,pgrp=1,timeout=0,minproto=5,maxproto=5,direct,pipe_ino=2\n" +
		"62 61 0:42 / /mnt/Windows rw,relatime shared:32 - ntfs3 /dev/sda1 rw\n" +
		"70 25 0:43 / /run/media/user/USB rw,nosuid,nodev,relatime shared:40 - vfat /dev/sdb1 rw\n"

	got := parseMountInfo(data)
	want := []string{"/", "/mnt/Windows", "/run/media/user/USB"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseMountInfo = %v, want %v", got, want)
	}
}

func TestParseMountInfoUnescapesPaths(t *testing.T) {
	data := "" +
		"25 1 0:22 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw\n" +
		"30 25 0:23 / /media/USB\\040Drive rw,relatime shared:2 - vfat /dev/sdb1 rw\n" +
		"31 25 0:24 / /media/Tab\\011Name rw,relatime shared:3 - ext4 /dev/sdc1 rw\n" +
		"32 25 0:25 / /media/Backslash\\134Dir rw,relatime shared:4 - ext4 /dev/sdd1 rw\n" +
		"33 25 0:26 / /media/Unicode\\040Caf\\303\\251 rw,relatime shared:5 - ext4 /dev/sde1 rw\n"

	got := parseMountInfo(data)
	want := []string{
		"/",
		"/media/USB Drive",
		"/media/Tab\tName",
		"/media/Backslash\\Dir",
		"/media/Unicode Café",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseMountInfo = %v, want %v", got, want)
	}
}

func TestParseMountInfoSkipsPseudoAndDuplicates(t *testing.T) {
	data := "" +
		"1 1 0:1 / /proc rw,nosuid shared:1 - proc proc rw\n" +
		"2 1 0:2 / /proc/sys/fs/binfmt_misc rw shared:2 - binfmt_misc none rw\n" +
		"3 1 0:3 / /sys rw shared:3 - sysfs sysfs rw\n" +
		"4 1 0:4 / /sys/fs/cgroup rw shared:4 - cgroup2 cgroup2 rw\n" +
		"5 1 0:5 / /dev rw shared:5 - devtmpfs udev rw\n" +
		"6 1 0:6 / /dev/pts rw shared:6 - devpts devpts rw\n" +
		"7 1 0:7 / /mnt/disk rw shared:7 - ext4 /dev/sda1 rw\n" +
		"8 1 0:7 / /mnt/disk rw shared:8 - ext4 /dev/sda1 rw\n"

	got := parseMountInfo(data)
	want := []string{"/mnt/disk"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseMountInfo = %v, want %v", got, want)
	}
}

func TestUnescapeMountPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/plain/path", "/plain/path"},
		{"/path\\040with\\040spaces", "/path with spaces"},
		{"/path\\134backslash", "/path\\backslash"},
		{"/path\\011tab", "/path\ttab"},
		{"/incomplete\\04", "/incomplete\\04"},
		{"/notoctal\\999", "/notoctal\\999"},
		{"/trailing\\", "/trailing\\"},
	}
	for _, tc := range cases {
		got := unescapeMountPath(tc.in)
		if got != tc.want {
			t.Errorf("unescapeMountPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
