package java

import "strings"

// zgcMinMajor is the first release where ZGC is a supported, non-experimental
// collector (JEP 377). Before it, -XX:+UseZGC needs UnlockExperimentalVMOptions
// on 11–14 and is unknown to Java 8, which refuses to start on an unrecognised
// -XX flag.
const zgcMinMajor = 15

// ZGCArgs returns the JVM flags that select ZGC on the given Java release, or
// nil when that release should be left on its default collector. A major of 0
// means the release is not known yet, and gets the form every supported
// release accepts.
//
// A Minecraft server is a latency workload: a tick has 50 ms, and a G1 pause
// on a multi-gigabyte heap eats a good part of it, which players feel as
// rubber-banding. ZGC keeps pauses under a millisecond regardless of heap
// size, at the cost of some throughput a server has to spare.
//
// The generational mode is what makes ZGC a clear win for Minecraft, whose
// garbage is overwhelmingly short-lived. It arrived as an opt-in on 21
// (JEP 439), became the default on 23 (JEP 474) and the only mode on 24
// (JEP 490), where the switch is gone and would only draw a warning.
func ZGCArgs(major int) []string {
	switch {
	case major > 0 && major < zgcMinMajor:
		return nil
	case major >= 21 && major <= 22:
		return []string{"-XX:+UseZGC", "-XX:+ZGenerational"}
	default:
		return []string{"-XX:+UseZGC"}
	}
}

// SupportsZGC reports whether a Java release can run with ZGCArgs.
func SupportsZGC(major int) bool { return major >= zgcMinMajor }

// SelectsGC reports whether the arguments pick any collector explicitly, so a
// caller offering a recommendation knows the user has already decided.
func SelectsGC(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-XX:+Use") && strings.HasSuffix(a, "GC") {
			return true
		}
	}
	return false
}
