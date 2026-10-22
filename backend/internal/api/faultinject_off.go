//go:build !faultinject

package api

// faultRoutes registers nothing in normal builds: the fault hook exists only under the faultinject build tag.
func (s *Server) faultRoutes() {}
