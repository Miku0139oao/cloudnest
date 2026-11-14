package services

// Close guard helpers.
//
// The window close hook needs to know whether a transfer is still running and
// whether the user already confirmed stopping it. These are package functions
// rather than Service methods so the Wails binding generator does not expose
// them to the frontend.

// ActiveTransfers counts the jobs the user would want a warning about.
func ActiveTransfers(s *Service) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, j := range s.jobs {
		if j.State == "running" || j.State == "paused" {
			n++
		}
	}
	return n
}

// AllowClose records that the user accepted stopping the transfers.
func AllowClose(s *Service) { s.closing.Store(true) }

// CloseConfirmed reports whether AllowClose has been called.
func CloseConfirmed(s *Service) bool { return s.closing.Load() }
