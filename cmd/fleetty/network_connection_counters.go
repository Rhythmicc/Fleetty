package main

type connectionNetworkCounters struct {
	cookie    uint64
	rx, tx    uint64
	uid       uint32
	available bool
}

func connectionNetworkKey(protocol, local, remote string) string {
	return protocol + "/" + local + "/" + remote
}
