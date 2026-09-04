package server

import (
	"mcmgr/internal/config"
	"mcmgr/internal/java"
)

// JVMArgs builds the JVM arguments for a server: the manifest's java.args
// (the heap among them), the collector java.gc asks for, then extra, which is
// what --java-arg adds for one run. javaMajor is the release of the Java that
// will run; 0 when it is not known.
//
// A collector named in java.args or extra wins over java.gc. Two -XX:+Use*GC
// flags make the JVM refuse to start, and the explicit flag is the more
// deliberate of the two, which also makes `--java-arg -XX:+UseG1GC` a way to
// try the default for a single run.
func JVMArgs(cfg *config.Config, javaMajor int, extra []string) []string {
	args := make([]string, 0, len(cfg.Java.Args)+len(extra)+2)
	args = append(args, cfg.Java.Args...)
	if cfg.Java.GC == config.GCZGC && !java.SelectsGC(cfg.Java.Args) && !java.SelectsGC(extra) {
		args = append(args, java.ZGCArgs(javaMajor)...)
	}
	return append(args, extra...)
}
