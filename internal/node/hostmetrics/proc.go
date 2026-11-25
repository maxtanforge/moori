package hostmetrics

import (
	"bufio"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
)

type cpuCounters struct{ total, idle uint64 }

func readCPU(r io.Reader) (cpuCounters, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return cpuCounters{}, err
		}
		return cpuCounters{}, errors.New("missing aggregate CPU counters")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuCounters{}, errors.New("invalid aggregate CPU counters")
	}
	var c cpuCounters
	// Guest counters are already included in user and nice.
	for i, field := range fields[1:min(len(fields), 9)] {
		ticks, err := strconv.ParseUint(field, 10, 64)
		if err != nil || math.MaxUint64-c.total < ticks {
			return cpuCounters{}, errors.New("invalid CPU counter")
		}
		c.total += ticks
		if i == 3 || i == 4 {
			c.idle += ticks
		}
	}
	return c, nil
}

func (c cpuCounters) utilization(previous cpuCounters) *uint16 {
	if previous.total == 0 || c.total <= previous.total || c.idle < previous.idle {
		return nil
	}
	elapsed, idle := c.total-previous.total, c.idle-previous.idle
	if idle > elapsed {
		return nil
	}
	u := uint16(10000 * float64(elapsed-idle) / float64(elapsed))
	return &u
}

func readMemory(r io.Reader) (uint64, uint64, error) {
	scanner := bufio.NewScanner(r)
	var total, available uint64
	var found bool
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "MemTotal:" && fields[0] != "MemAvailable:" {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return 0, 0, errors.New("invalid memory units")
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > math.MaxInt64/1024 {
			return 0, 0, errors.New("invalid memory counter")
		}
		if fields[0] == "MemTotal:" {
			total = value * 1024
		} else {
			available = value * 1024
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if total == 0 || !found || available > total {
		return 0, 0, errors.New("memory availability is unknown")
	}
	return total, available, nil
}
