package server

import (
	"reflect"
	"testing"

	"mcmgr/internal/config"
)

func TestJVMArgs(t *testing.T) {
	cases := []struct {
		name  string
		java  config.Java
		major int
		extra []string
		want  []string
	}{
		{"default gc", config.Java{Args: []string{"-Xmx4G"}}, 25, nil, []string{"-Xmx4G"}},
		{"zgc on 25", config.Java{Args: []string{"-Xmx4G"}, GC: config.GCZGC}, 25, nil,
			[]string{"-Xmx4G", "-XX:+UseZGC"}},
		{"zgc on 21 is generational", config.Java{Args: []string{"-Xmx4G"}, GC: config.GCZGC}, 21, nil,
			[]string{"-Xmx4G", "-XX:+UseZGC", "-XX:+ZGenerational"}},
		{"zgc on unknown java", config.Java{GC: config.GCZGC}, 0, nil, []string{"-XX:+UseZGC"}},
		{"zgc on java 8 is dropped", config.Java{Args: []string{"-Xmx1G"}, GC: config.GCZGC}, 8, nil,
			[]string{"-Xmx1G"}},
		{"explicit collector in args wins", config.Java{Args: []string{"-XX:+UseG1GC"}, GC: config.GCZGC}, 25, nil,
			[]string{"-XX:+UseG1GC"}},
		{"explicit collector in extra wins", config.Java{Args: []string{"-Xmx4G"}, GC: config.GCZGC}, 25,
			[]string{"-XX:+UseShenandoahGC"}, []string{"-Xmx4G", "-XX:+UseShenandoahGC"}},
		{"extra goes last", config.Java{Args: []string{"-Xmx4G"}, GC: config.GCZGC}, 25,
			[]string{"-Dfoo=bar"}, []string{"-Xmx4G", "-XX:+UseZGC", "-Dfoo=bar"}},
	}
	for _, c := range cases {
		cfg := &config.Config{Java: c.java}
		if got := JVMArgs(cfg, c.major, c.extra); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: JVMArgs = %v, want %v", c.name, got, c.want)
		}
	}
}
