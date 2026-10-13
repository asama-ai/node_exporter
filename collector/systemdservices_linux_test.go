// Copyright 2015 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestNewSystemdCollectorsSkipDial(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewSystemdServicesCollector(logger); err != nil {
		t.Fatalf("NewSystemdServicesCollector: %v", err)
	}
	if _, err := NewSystemdInfoCollector(logger); err != nil {
		t.Fatalf("NewSystemdInfoCollector: %v", err)
	}
}

func TestSystemdConnGetReusesHealthy(t *testing.T) {
	dummy := &dbus.Conn{}
	s := &systemdConn{conn: dummy}
	got, err := s.get()
	if err != nil {
		t.Fatal(err)
	}
	if got != dummy {
		t.Fatal("expected stored connection to be reused")
	}
}

func TestSystemdConnGetAfterClose(t *testing.T) {
	s := &systemdConn{}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := s.get(); !errors.Is(err, errSystemdConnClosed) {
		t.Fatalf("get after close: got %v want %v", err, errSystemdConnClosed)
	}
}

func TestListSystemdUnitsNilConn(t *testing.T) {
	if _, err := listSystemdUnits(nil); !errors.Is(err, errSystemdConnClosed) {
		t.Fatalf("listSystemdUnits(nil): got %v want %v", err, errSystemdConnClosed)
	}
}

func TestSystemdCollectorsUpdateClosed(t *testing.T) {
	ch := make(chan prometheus.Metric, 1)
	svc := &systemdServicesCollector{
		serviceState: prometheus.NewDesc("node_systemd_service_state", "x", []string{"name"}, nil),
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("services Close: %v", err)
	}
	if err := svc.Update(ch); !errors.Is(err, errSystemdConnClosed) {
		t.Fatalf("services Update after Close: got %v want %v", err, errSystemdConnClosed)
	}

	info := &systemdInfoCollector{}
	if err := info.Close(); err != nil {
		t.Fatalf("info Close: %v", err)
	}
	if err := info.Update(ch); !errors.Is(err, errSystemdConnClosed) {
		t.Fatalf("info Update after Close: got %v want %v", err, errSystemdConnClosed)
	}
}

func TestSystemdCollectorsUpdateDialFailure(t *testing.T) {
	dialErr := errors.New("dbus unavailable")
	dial := func() (*dbus.Conn, error) { return nil, dialErr }
	ch := make(chan prometheus.Metric, 1)

	svc := &systemdServicesCollector{
		serviceState: prometheus.NewDesc("node_systemd_service_state", "x", []string{"name"}, nil),
		sc:           systemdConn{dial: dial},
	}
	err := svc.Update(ch)
	if err == nil || !strings.Contains(err.Error(), "couldn't get dbus connection") || !errors.Is(err, dialErr) {
		t.Fatalf("services Update dial failure: %v", err)
	}

	info := &systemdInfoCollector{sc: systemdConn{dial: dial}}
	err = info.Update(ch)
	if err == nil || !strings.Contains(err.Error(), "couldn't get dbus connection") || !errors.Is(err, dialErr) {
		t.Fatalf("info Update dial failure: %v", err)
	}
}

func TestSkipInactiveOneshotLookupOnlyWhenInactive(t *testing.T) {
	lookups := 0
	lookup := func() string {
		lookups++
		return "oneshot"
	}
	for _, state := range []string{"active", "failed", "activating", "deactivating", "reloading"} {
		if skipInactiveOneshotLazy(state, lookup) {
			t.Fatalf("%s: must emit", state)
		}
	}
	if lookups != 0 {
		t.Fatalf("non-inactive type lookups=%d want 0", lookups)
	}
	if !skipInactiveOneshotLazy("inactive", lookup) {
		t.Fatal("inactive oneshot must skip")
	}
	if lookups != 1 {
		t.Fatalf("inactive oneshot lookups=%d want 1", lookups)
	}
	if skipInactiveOneshotLazy("inactive", func() string { return "simple" }) {
		t.Fatal("inactive simple is a down long-running unit")
	}
}

func TestRetainUnitTypesDropsAbsent(t *testing.T) {
	c := &systemdServicesCollector{unitType: map[string]string{
		"gone.service":   "oneshot",
		"keep.service":   "oneshot",
		"active.service": "simple",
	}}
	c.retainUnitTypes(map[string]struct{}{
		"keep.service":   {},
		"active.service": {},
	})
	if _, ok := c.unitType["gone.service"]; ok {
		t.Fatal("stale type for absent unit must be dropped")
	}
	if c.unitType["keep.service"] != "oneshot" {
		t.Fatalf("keep: %q", c.unitType["keep.service"])
	}
	if c.unitType["active.service"] != "simple" {
		t.Fatalf("active: %q", c.unitType["active.service"])
	}
}

func TestSkipInactiveOneshot(t *testing.T) {
	if !skipInactiveOneshot("inactive", "oneshot") {
		t.Fatal("inactive oneshot must skip")
	}
	if !skipInactiveOneshot("Inactive", "Oneshot") {
		t.Fatal("case-insensitive")
	}
	if skipInactiveOneshot("inactive", "simple") {
		t.Fatal("inactive simple is a down long-running unit")
	}
	if skipInactiveOneshot("failed", "oneshot") {
		t.Fatal("failed oneshot must emit")
	}
	if skipInactiveOneshot("active", "oneshot") {
		t.Fatal("active oneshot (RemainAfterExit) must emit")
	}
	if skipInactiveOneshot("activating", "oneshot") {
		t.Fatal("activating oneshot must emit")
	}
	if skipInactiveOneshot("inactive", "") {
		t.Fatal("unknown type must emit (fail-open)")
	}
}

func TestSkipInactiveNotFound(t *testing.T) {
	if !skipInactiveNotFound("inactive", "not-found") {
		t.Fatal("inactive not-found must skip")
	}
	if !skipInactiveNotFound("Inactive", "Not-Found") {
		t.Fatal("case-insensitive")
	}
	if skipInactiveNotFound("inactive", "loaded") {
		t.Fatal("inactive loaded is a down long-running unit")
	}
	if skipInactiveNotFound("failed", "not-found") {
		t.Fatal("failed not-found must emit")
	}
	if skipInactiveNotFound("activating", "not-found") {
		t.Fatal("activating not-found must emit")
	}
	if skipInactiveNotFound("inactive", "") {
		t.Fatal("unknown load state must emit (fail-open)")
	}
}

func TestServiceTypeFromProps(t *testing.T) {
	if got := serviceTypeFromProps(nil); got != "" {
		t.Fatalf("nil props: %q", got)
	}
	if got := serviceTypeFromProps(map[string]any{"Type": "oneshot"}); got != "oneshot" {
		t.Fatalf("Type: %q", got)
	}
	if got := serviceTypeFromProps(map[string]any{"Type": ""}); got != "" {
		t.Fatalf("empty Type: %q", got)
	}
}

func TestNRestartsFromProps(t *testing.T) {
	if _, ok := nRestartsFromProps(nil); ok {
		t.Fatal("missing NRestarts should be ok on older systemd")
	}
	if _, ok := nRestartsFromProps(map[string]any{"Type": "simple"}); ok {
		t.Fatal("expected missing NRestarts")
	}
	got, ok := nRestartsFromProps(map[string]any{"NRestarts": uint32(3)})
	if !ok || got != 3 {
		t.Fatalf("NRestarts: got %v ok=%v", got, ok)
	}
	if _, ok := nRestartsFromProps(map[string]any{"NRestarts": "nope"}); ok {
		t.Fatal("unexpected type should not parse")
	}
}

func TestSystemdInfoReadsTypeOnceAndRestartsEachTick(t *testing.T) {
	var typeCalls, restartCalls int
	c := newTestSystemdInfoCollector(func(_ context.Context, _ *dbus.Conn, unit, name string) (*dbus.Property, error) {
		switch name {
		case "Type":
			typeCalls++
			return &dbus.Property{Name: name, Value: godbus.MakeVariant("simple")}, nil
		case "NRestarts":
			restartCalls++
			return &dbus.Property{Name: name, Value: godbus.MakeVariant(uint32(2))}, nil
		default:
			t.Fatalf("unexpected property %s for %s", name, unit)
			return nil, nil
		}
	})
	unit := dbus.UnitStatus{Name: "a.service", SubState: "running", LoadState: "loaded"}
	for tick := 0; tick < 2; tick++ {
		ch := make(chan prometheus.Metric, 8)
		c.collectAnalysisMetrics(context.Background(), ch, nil, unit)
		got := takeMetrics(ch)
		if len(got) != 4 {
			t.Fatalf("tick %d: metrics=%d want 4", tick, len(got))
		}
		if typ := metricLabel(t, got[0], "type"); typ != "simple" {
			t.Fatalf("tick %d: type=%q", tick, typ)
		}
		if v := metricValue(t, got[3]); v != 2 {
			t.Fatalf("tick %d: NRestarts=%v", tick, v)
		}
	}
	if typeCalls != 1 || restartCalls != 2 {
		t.Fatalf("Type calls=%d want 1, NRestarts calls=%d want 2", typeCalls, restartCalls)
	}
}

func TestSystemdInfoTypeMissDoesNotCache(t *testing.T) {
	typeCalls := 0
	c := newTestSystemdInfoCollector(func(_ context.Context, _ *dbus.Conn, _, name string) (*dbus.Property, error) {
		if name == "Type" {
			typeCalls++
			return nil, errors.New("no such unit")
		}
		return nil, errors.New("no NRestarts")
	})
	unit := dbus.UnitStatus{Name: "missing.service", SubState: "dead", LoadState: "not-found"}
	for tick := 0; tick < 2; tick++ {
		ch := make(chan prometheus.Metric, 8)
		c.collectAnalysisMetrics(context.Background(), ch, nil, unit)
		got := takeMetrics(ch)
		if len(got) != 3 {
			t.Fatalf("tick %d: metrics=%d want 3 (no restart counter)", tick, len(got))
		}
		if typ := metricLabel(t, got[0], "type"); typ != "unknown" {
			t.Fatalf("tick %d: type=%q", tick, typ)
		}
	}
	if typeCalls != 2 {
		t.Fatalf("Type calls=%d want 2 when the read fails", typeCalls)
	}
}

func TestSystemdInfoDropsCachedTypeWhenUnitLeaves(t *testing.T) {
	c := newTestSystemdInfoCollector(nil)
	c.unitType["gone.service"] = cachedServiceType{value: "simple", at: time.Now()}
	c.unitType["keep.service"] = cachedServiceType{value: "notify", at: time.Now()}
	c.retainUnitTypes(map[string]struct{}{"keep.service": {}})
	if _, ok := c.unitType["gone.service"]; ok {
		t.Fatal("cached Type for a unit that left the list must be dropped")
	}
	if c.unitType["keep.service"].value != "notify" {
		t.Fatalf("keep: %q", c.unitType["keep.service"].value)
	}
}

func TestSystemdInfoRefreshesTypeAfterTTL(t *testing.T) {
	calls := 0
	c := newTestSystemdInfoCollector(func(_ context.Context, _ *dbus.Conn, _, name string) (*dbus.Property, error) {
		if name != "Type" {
			return &dbus.Property{Name: name, Value: godbus.MakeVariant(uint32(1))}, nil
		}
		calls++
		return &dbus.Property{Name: name, Value: godbus.MakeVariant("notify")}, nil
	})
	c.unitType["a.service"] = cachedServiceType{
		value: "simple",
		at:    time.Now().Add(-systemdInfoTypeTTL),
	}
	unit := dbus.UnitStatus{Name: "a.service", SubState: "running", LoadState: "loaded"}
	ch := make(chan prometheus.Metric, 8)
	c.collectAnalysisMetrics(context.Background(), ch, nil, unit)
	got := takeMetrics(ch)
	if typ := metricLabel(t, got[0], "type"); typ != "notify" {
		t.Fatalf("type=%q want notify after TTL", typ)
	}
	ch = make(chan prometheus.Metric, 8)
	c.collectAnalysisMetrics(context.Background(), ch, nil, unit)
	got = takeMetrics(ch)
	if typ := metricLabel(t, got[0], "type"); typ != "notify" {
		t.Fatalf("cached type=%q", typ)
	}
	if calls != 1 {
		t.Fatalf("Type calls=%d want 1 refresh then a cache hit", calls)
	}
}

func newTestSystemdInfoCollector(prop func(context.Context, *dbus.Conn, string, string) (*dbus.Property, error)) *systemdInfoCollector {
	return &systemdInfoCollector{
		serviceInfo: prometheus.NewDesc(
			"node_systemd_service_info", "x", []string{"name", "type"}, nil,
		),
		serviceSubState: prometheus.NewDesc(
			"node_systemd_service_sub_state", "x", []string{"name"}, nil,
		),
		serviceLoadState: prometheus.NewDesc(
			"node_systemd_service_load_state", "x", []string{"name"}, nil,
		),
		serviceRestartTotal: prometheus.NewDesc(
			"node_systemd_service_restart_total", "x", []string{"name"}, nil,
		),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		unitType:    map[string]cachedServiceType{},
		serviceProp: prop,
	}
}

func takeMetrics(ch <-chan prometheus.Metric) []prometheus.Metric {
	var out []prometheus.Metric
	for {
		select {
		case m := <-ch:
			out = append(out, m)
		default:
			return out
		}
	}
}

func writeMetric(t *testing.T, m prometheus.Metric) dto.Metric {
	t.Helper()
	var dm dto.Metric
	if err := m.Write(&dm); err != nil {
		t.Fatal(err)
	}
	return dm
}

func metricLabel(t *testing.T, m prometheus.Metric, name string) string {
	t.Helper()
	dm := writeMetric(t, m)
	for _, lp := range dm.Label {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	t.Fatalf("label %s missing", name)
	return ""
}

func metricValue(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	dm := writeMetric(t, m)
	if dm.Counter != nil {
		return dm.Counter.GetValue()
	}
	if dm.Gauge != nil {
		return dm.Gauge.GetValue()
	}
	t.Fatal("metric has no gauge or counter")
	return 0
}
