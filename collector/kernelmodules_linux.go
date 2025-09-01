package collector

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

type kernelModulesCollector struct {
	moduleState    *prometheus.Desc
	moduleRefcount *prometheus.Desc
	moduleSize     *prometheus.Desc
	logger         *slog.Logger
}

func init() {
	registerCollector("kernelmodules", defaultDisabled, NewKernelModulesCollector)
}

// NewKernelModulesCollector returns a new Collector exposing kernel module information.
func NewKernelModulesCollector(logger *slog.Logger) (Collector, error) {
	return &kernelModulesCollector{
		moduleState: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "kernel_module", "state"),
			"State of the kernel module: 1 = Live (fully loaded and functioning), "+
				"0 = Loading (module load in progress), "+
				"-1 = Unloading (module removal in progress).",
			[]string{
				"module", // Module name
			},
			nil,
		),
		moduleRefcount: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "kernel_module", "refcount"),
			"Number of references to the kernel module (usage count).",
			[]string{
				"module", // Module name
			},
			nil,
		),
		moduleSize: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "kernel_module", "size_bytes"),
			"Memory usage of the kernel module in bytes.",
			[]string{
				"module", // Module name
			},
			nil,
		),
		logger: logger,
	}, nil
}

// Update implements Collector and exposes kernel module metrics from /proc/modules.
func (c *kernelModulesCollector) Update(ch chan<- prometheus.Metric) error {
	file, err := os.Open("/proc/modules")
	if err != nil {
		return fmt.Errorf("failed to read /proc/modules: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) < 5 {
			continue
		}

		module := parts[0]
		sizeStr := parts[1]
		refcountStr := parts[2]
		state := parts[4]

		// Parse size
		size, err := strconv.ParseFloat(sizeStr, 64)
		if err != nil {
			c.logger.Warn("failed to parse kernel module size", "module", module, "size", sizeStr, "error", err)
			continue
		}

		// Parse refcount
		refcount, err := strconv.ParseFloat(refcountStr, 64)
		if err != nil {
			c.logger.Warn("failed to parse kernel module refcount", "module", module, "refcount", refcountStr, "error", err)
			continue
		}

		// Convert state to numeric value
		var stateValue float64
		switch state {
		case "Live":
			stateValue = 1
		case "Loading":
			stateValue = 0
		case "Unloading":
			stateValue = -1
		default:
			c.logger.Warn("unknown kernel module state", "module", module, "state", state)
			continue
		}

		// State metric (numeric value)
		ch <- prometheus.MustNewConstMetric(
			c.moduleState,
			prometheus.GaugeValue,
			stateValue,
			module, // module
		)

		// Size metric (can change over time)
		ch <- prometheus.MustNewConstMetric(
			c.moduleSize,
			prometheus.GaugeValue,
			size,
			module, // module
		)

		// Refcount metric (can change over time)
		ch <- prometheus.MustNewConstMetric(
			c.moduleRefcount,
			prometheus.GaugeValue,
			refcount,
			module, // module
		)
	}

	return scanner.Err()
}
