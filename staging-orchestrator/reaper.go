package main

import "time"

func reaper_loop(r *registry) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()

	for range tick.C {
		r.reap_idle()
	}
}

func (r *registry) reap_idle() {
	r.mu.Lock()
	var toReap []*instance

	for ticket, inst := range r.m {
		select {
		case <-inst.ready:
		default:
			continue // still launching, leave it alone
		}
		if inst.err != nil {
			continue // already failed; get_or_launch already cleaned this up
		}
		if inst.idle_for() > r.cfg.idleTimeout {
			toReap = append(toReap, inst)
			delete(r.m, ticket)
		}
	}
	r.mu.Unlock()

	for _, inst := range toReap {
		kill_instance(inst, "idle")
	}
}
