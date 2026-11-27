//go:build !linux

package wireless

func ApplyCarRoutes(string, func(string, ...any)) error   { return nil }
func RestoreCarRoutes(string, func(string, ...any)) error { return nil }
