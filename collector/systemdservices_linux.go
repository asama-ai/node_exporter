package collector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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
			prometheus.BuildFQName(namespace, "systemd_services", "info"),
			"Static systemd service information via D-Bus API. Value is always 1.",
			[]string{"name", "type"},
			nil,
		),
		serviceState: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "systemd_services", "state"),
			"Systemd service state: 1 = active, 2 = reloading, 3 = inactive, 4 = failed, 5 = activating, 6 = deactivating.",
			[]string{"name"},
			nil,
		),
		serviceSubState: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "systemd_services", "sub_state"),
			"Systemd service sub-state: 1 = running, 2 = exited, 3 = failed, 4 = dead, 5 = start, 6 = stop, 7 = reload, 8 = auto-restart.",
			[]string{"name"},
			nil,
		),
		serviceLoadState: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "systemd_services", "load_state"),
			"Systemd service load state: 1 = loaded, 2 = error, 3 = masked, 4 = not-found.",
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
	units, err := conn.ListUnitsContext(context.TODO())
	if err != nil {
		return nil, err
	}
	return units, nil
}

func (c *systemdServicesCollector) collectServiceMetrics(conn *dbus.Conn, ch chan<- prometheus.Metric, unit dbus.UnitStatus) error {
	serviceType := "unknown"
	typeProperty, err := conn.GetUnitTypePropertyContext(context.TODO(), unit.Name, "Service", "Type")
	if err == nil {
		serviceType = typeProperty.Value.Value().(string)
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
	if stateValue, err := parseSystemdState(unit.ActiveState); err == nil {
		ch <- prometheus.MustNewConstMetric(
			c.serviceState,
			prometheus.GaugeValue,
			stateValue,
			unit.Name,
		)
	} else {
		c.logger.Debug("failed to parse systemd state", "unit", unit.Name, "state", unit.ActiveState, "error", err)
	}

	// Sub-state metric (numeric value)
	if subStateValue, err := parseSystemdSubState(unit.SubState); err == nil {
		ch <- prometheus.MustNewConstMetric(
			c.serviceSubState,
			prometheus.GaugeValue,
			subStateValue,
			unit.Name,
		)
	} else {
		c.logger.Debug("failed to parse systemd sub-state", "unit", unit.Name, "sub_state", unit.SubState, "error", err)
	}

	// Load state metric (numeric value)
	if loadStateValue, err := parseSystemdLoadState(unit.LoadState); err == nil {
		ch <- prometheus.MustNewConstMetric(
			c.serviceLoadState,
			prometheus.GaugeValue,
			loadStateValue,
			unit.Name,
		)
	} else {
		c.logger.Debug("failed to parse systemd load state", "unit", unit.Name, "load_state", unit.LoadState, "error", err)
	}

	return nil
}

// parseSystemdState converts systemd state string to numeric value
func parseSystemdState(state string) (float64, error) {
	switch strings.ToLower(state) {
	case "active":
		return 1, nil
	case "reloading":
		return 2, nil
	case "inactive":
		return 3, nil
	case "failed":
		return 4, nil
	case "activating":
		return 5, nil
	case "deactivating":
		return 6, nil
	default:
		return 0, fmt.Errorf("unknown systemd state: %s", state)
	}
}

// parseSystemdSubState converts systemd sub-state string to numeric value
func parseSystemdSubState(subState string) (float64, error) {
	switch strings.ToLower(subState) {
	case "running":
		return 1, nil
	case "exited":
		return 2, nil
	case "failed":
		return 3, nil
	case "dead":
		return 4, nil
	case "start":
		return 5, nil
	case "stop":
		return 6, nil
	case "reload":
		return 7, nil
	case "auto-restart":
		return 8, nil
	default:
		return 0, fmt.Errorf("unknown systemd sub-state: %s", subState)
	}
}

// parseSystemdLoadState converts systemd load state string to numeric value
func parseSystemdLoadState(loadState string) (float64, error) {
	switch strings.ToLower(loadState) {
	case "loaded":
		return 1, nil
	case "error":
		return 2, nil
	case "masked":
		return 3, nil
	case "not-found":
		return 4, nil
	default:
		return 0, fmt.Errorf("unknown systemd load state: %s", loadState)
	}
}

func (c *systemdServicesCollector) Close() error {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	return nil
}
