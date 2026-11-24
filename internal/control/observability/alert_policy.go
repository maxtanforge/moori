package observability

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

const alertRecoveryMargin = 5

// Policy controls sustained resource alerts and whether transitions are queued for delivery.
type Policy struct {
	CPUPercent    int
	MemoryPercent int
	SustainedFor  time.Duration
	Notify        bool
}

// DefaultPolicy uses 90 percent CPU and memory utilization for at least two minutes.
func DefaultPolicy() Policy {
	return Policy{CPUPercent: 90, MemoryPercent: 90, SustainedFor: 2 * time.Minute}
}

// PolicyFromEnv reads optional administrator overrides.
func PolicyFromEnv(getenv func(string) string) (Policy, error) {
	policy := DefaultPolicy()
	for _, setting := range []struct {
		env   string
		value *int
	}{
		{"MOORI_ALERT_CPU_PERCENT", &policy.CPUPercent},
		{"MOORI_ALERT_MEMORY_PERCENT", &policy.MemoryPercent},
	} {
		value := strings.TrimSpace(getenv(setting.env))
		if value == "" {
			continue
		}
		percent, err := strconv.Atoi(value)
		if err != nil || percent < 1 || percent > 100 {
			return Policy{}, errors.New(setting.env + " must be a whole percent from 1 to 100")
		}
		*setting.value = percent
	}
	if value := strings.TrimSpace(getenv("MOORI_ALERT_SUSTAINED_FOR")); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil || duration < time.Second || duration > time.Hour {
			return Policy{}, errors.New("MOORI_ALERT_SUSTAINED_FOR must be a duration between 1s and 1h")
		}
		policy.SustainedFor = duration
	}
	return policy, nil
}

func (p Policy) valid() bool {
	return p.CPUPercent >= 1 && p.CPUPercent <= 100 &&
		p.MemoryPercent >= 1 && p.MemoryPercent <= 100 &&
		p.SustainedFor >= time.Second && p.SustainedFor <= time.Hour
}

func belowRecovery(cpu uint16, percent int) bool {
	return int(cpu) <= max(0, percent-alertRecoveryMargin)*100
}

// percentOf rounds in the requested direction without overflowing large host capacities.
func percentOf(total uint64, percent int, roundUp bool) uint64 {
	p := uint64(percent)
	remainder := total % 100 * p
	if roundUp {
		remainder += 99
	}
	return total/100*p + remainder/100
}
