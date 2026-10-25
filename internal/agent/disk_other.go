//go:build !unix

package agent

func diskSpace(dir string) (total, free int64) { return 0, 0 }
