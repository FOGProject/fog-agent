package power

import "time"

// The actions a schedule or an on-demand request carries. They are the
// reboot coordinator's modes by the same names.
const (
	ActionShutdown = "shutdown"
	ActionReboot   = "reboot"
)

// Schedule is one resolved schedule: the host's own or a group's grant.
type Schedule struct {
	Cron   string `json:"cron"`
	Action string `json:"action"`
}

// OnDemand is an action an admin asked for now. The id is the server's
// row, consumed when the agent reports that it accepted it.
type OnDemand struct {
	ID     int    `json:"id"`
	Action string `json:"action"`
}

// Policy is the power block of the desired state.
type Policy struct {
	Schedules []Schedule `json:"schedules"`
	OnDemand  []OnDemand `json:"ondemand"`
}

// Next is the earliest firing strictly after t across schedules, with
// the schedule it belongs to; false when none has a firing in the
// coming year. A schedule whose expression does not parse is skipped:
// the server built it from five validated fields, so this is defensive.
func Next(schedules []Schedule, t time.Time) (time.Time, Schedule, bool) {
	var (
		best  time.Time
		which Schedule
		found bool
	)
	for _, s := range schedules {
		c, err := Parse(s.Cron)
		if err != nil {
			continue
		}
		n, ok := c.Next(t)
		if !ok {
			continue
		}
		if !found || n.Before(best) {
			best, which, found = n, s, true
		}
	}
	return best, which, found
}
