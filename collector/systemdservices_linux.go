package collector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/prometheus/client_golang/prometheus"
)

type systemdServicesCollector struct {
	serviceInfo      *prometheus.Desc
	serviceState     *prometheus.Desc
	serviceSubState  *prometheus.Desc
	serviceLoadState *prometheus.Desc
	logger           *slog.Logger
	conn             *dbus.Conn
}

func init() {
	registerCollector("systemdservices", defaultDisabled, NewSystemdServicesCollector)
}

func NewSystemdServicesCollector(logger *slog.Logger) (Collector, error) {
	conn, err := newSystemdDbusConn()
	if err != nil {
		return nil, fmt.Errorf("couldn't get dbus connection: %w", err)
	}

	return &systemdServicesCollector{
		serviceInfo: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "systemd_service", "info"),
			"Static systemd service information via D-Bus API. Value is always 1.",
			[]string{"name", "type"},
			nil,
		),
		serviceState: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "systemd_service", "state"),
			"Systemd service state: 0 = unknown, 1 = active, 2 = reloading, 3 = inactive, 4 = failed, 5 = activating, 6 = deactivating.",
			[]string{"name"},
			nil,
		),
		serviceSubState: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "systemd_service", "sub_state"),
			"Systemd service sub-state: 0 = unknown, 1 = running, 2 = exited, 3 = failed, 4 = dead, 5 = start, 6 = stop, 7 = reload, 8 = auto-restart.",
			[]string{"name"},
			nil,
		),
		serviceLoadState: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "systemd_service", "load_state"),
			"Systemd service load state: 0 = unknown, 1 = loaded, 2 = error, 3 = masked, 4 = not-found.",
			[]string{"name"},
			nil,
		),
		logger: logger,
		conn:   conn,
	}, nil
}

func (c *systemdServicesCollector) Update(ch chan<- prometheus.Metric) error {
	units, err := c.getAllUnits(c.conn)
	if err != nil {
		return fmt.Errorf("couldn't get units: %w", err)
	}

	for _, unit := range units {
		if !strings.HasSuffix(unit.Name, ".service") {
			continue
		}

		if err := c.collectServiceMetrics(c.conn, ch, unit); err != nil {
			c.logger.Debug("failed to collect metrics for unit", "unit", unit.Name, "error", err)
			continue
		}
	}

	return nil
}

func (c *systemdServicesCollector) getAllUnits(conn *dbus.Conn) ([]dbus.UnitStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	units, err := conn.ListUnitsContext(ctx)
	if err != nil {
		return nil, err
	}
	return units, nil
}

func (c *systemdServicesCollector) collectServiceMetrics(conn *dbus.Conn, ch chan<- prometheus.Metric, unit dbus.UnitStatus) error {
	serviceType := "unknown"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	typeProperty, err := conn.GetUnitTypePropertyContext(ctx, unit.Name, "Service", "Type")
	if err == nil {
		if v, ok := typeProperty.Value.Value().(string); ok && v != "" {
			serviceType = v
		}
	}

	// Info metric (static information, always 1)
	ch <- prometheus.MustNewConstMetric(
		c.serviceInfo,
		prometheus.GaugeValue,
		1,
		unit.Name,
		serviceType,
	)

	// State metric (numeric value)
	stateValue := parseSystemdState(unit.ActiveState)
	ch <- prometheus.MustNewConstMetric(
		c.serviceState,
		prometheus.GaugeValue,
		stateValue,
		unit.Name,
	)

	// Sub-state metric (numeric value)
	subStateValue := parseSystemdSubState(unit.SubState)
	ch <- prometheus.MustNewConstMetric(
		c.serviceSubState,
		prometheus.GaugeValue,
		subStateValue,
		unit.Name,
	)

	// Load state metric (numeric value)
	loadStateValue := parseSystemdLoadState(unit.LoadState)
	ch <- prometheus.MustNewConstMetric(
		c.serviceLoadState,
		prometheus.GaugeValue,
		loadStateValue,
		unit.Name,
	)

	return nil
}

// parseSystemdState converts systemd state string to numeric value
func parseSystemdState(state string) float64 {
	switch strings.ToLower(state) {
	case "active":
		return 1
	case "reloading":
		return 2
	case "inactive":
		return 3
	case "failed":
		return 4
	case "activating":
		return 5
	case "deactivating":
		return 6
	default:
		return 0 // unknown
	}
}

// parseSystemdSubState converts systemd sub-state string to numeric value
func parseSystemdSubState(subState string) float64 {
	switch strings.ToLower(subState) {
	case "running":
		return 1
	case "exited":
		return 2
	case "failed":
		return 3
	case "dead":
		return 4
	case "start":
		return 5
	case "stop":
		return 6
	case "reload":
		return 7
	case "auto-restart":
		return 8
	default:
		return 0 // unknown
	}
}

// parseSystemdLoadState converts systemd load state string to numeric value
func parseSystemdLoadState(loadState string) float64 {
	switch strings.ToLower(loadState) {
	case "loaded":
		return 1
	case "error":
		return 2
	case "masked":
		return 3
	case "not-found":
		return 4
	default:
		return 0 // unknown
	}
}

func (c *systemdServicesCollector) Close() error {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	return nil
}
