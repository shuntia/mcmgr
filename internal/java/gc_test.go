package java

import (
	"reflect"
	"testing"
)

func TestZGCArgs(t *testing.T) {
	cases := []struct {
		major int
		want  []string
	}{
		{0, []string{"-XX:+UseZGC"}},
		{8, nil},
		{11, nil},
		{14, nil},
		{15, []string{"-XX:+UseZGC"}},
		{17, []string{"-XX:+UseZGC"}},
		{21, []string{"-XX:+UseZGC", "-XX:+ZGenerational"}},
		{22, []string{"-XX:+UseZGC", "-XX:+ZGenerational"}},
		{23, []string{"-XX:+UseZGC"}},
		{25, []string{"-XX:+UseZGC"}},
	}
	for _, c := range cases {
		if got := ZGCArgs(c.major); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ZGCArgs(%d) = %v, want %v", c.major, got, c.want)
		}
		if got := SupportsZGC(c.major); got != (c.major >= zgcMinMajor) {
			t.Errorf("SupportsZGC(%d) = %v", c.major, got)
		}
	}
}

func TestSelectsGC(t *testing.T) {
	for _, args := range [][]string{
		{"-XX:+UseG1GC"},
		{"-Xmx2G", "-XX:+UseShenandoahGC"},
		{"-XX:+UseZGC"},
		{"-XX:+UseParallelGC"},
	} {
		if !SelectsGC(args) {
			t.Errorf("SelectsGC(%v) = false", args)
		}
	}
	for _, args := range [][]string{
		nil,
		{"-Xmx2G"},
		{"-XX:+UseStringDeduplication"},
		{"-XX:+ZGenerational"},
	} {
		if SelectsGC(args) {
			t.Errorf("SelectsGC(%v) = true", args)
		}
	}
}
