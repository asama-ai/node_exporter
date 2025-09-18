package collector

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	pciIdsPaths = []string{
		"/usr/share/misc/pci.ids",
		"/usr/share/hwdata/pci.ids",
	}
	pciVendors    = make(map[string]string)
	pciDevices    = make(map[string]map[string]string)
	pciSubsystems = make(map[string]map[string]string)
	pciClasses    = make(map[string]string)
	pciSubclasses = make(map[string]string)
)

type pcieCollector struct {
	info          *prometheus.Desc
	currentSpeed  *prometheus.Desc
	currentWidth  *prometheus.Desc
	maxSpeed      *prometheus.Desc
	maxWidth      *prometheus.Desc
	powerState    *prometheus.Desc
	d3coldAllowed *prometheus.Desc
	logger        *slog.Logger
}

func init() {
	registerCollector("pcie", defaultDisabled, NewPCIeCollector)
	loadPCIIds()
}

func NewPCIeCollector(logger *slog.Logger) (Collector, error) {
	return &pcieCollector{
		info: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "pcie_device", "info"),
			"Static PCIe device information from /sys/bus/pci/devices/.",
			[]string{
				"slot", // Changed from "device"
				"vendor_id",
				"vendor_name", // Changed from "vendor"
				"device_id",
				"device_name", // Changed from "device"
				"subsystem_vendor_id",
				"subsystem_vendor_name", // Changed from "subsystem_vendor"
				"subsystem_device_id",
				"subsystem_device_name", // Changed from "subsystem_device"
				"class_id",
				"class",
				"revision",
			},
			nil,
		),
		currentSpeed: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "pcie_slot", "current_speed_gts"),
			"Current PCIe link speed in GT/s (e.g., 2.5, 5.0, 8.0, 16.0).",
			[]string{
				"slot",
			},
			nil,
		),
		currentWidth: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "pcie_slot", "current_width_lanes"),
			"Current PCIe link width in lanes (e.g., 1, 2, 4, 8, 16).",
			[]string{
				"slot",
			},
			nil,
		),
		maxSpeed: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "pcie_slot", "max_speed_gts"),
			"Maximum PCIe link speed in GT/s (e.g., 2.5, 5.0, 8.0, 16.0).",
			[]string{
				"slot",
			},
			nil,
		),
		maxWidth: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "pcie_slot", "max_width_lanes"),
			"Maximum PCIe link width in lanes (e.g., 1, 2, 4, 8, 16).",
			[]string{
				"slot",
			},
			nil,
		),
		powerState: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "pcie_slot", "power_state"),
			"PCIe device power state: 0 = D0 (fully powered), 1 = D1, 2 = D2, 3 = D3hot, 4 = D3cold (lowest power).",
			[]string{
				"slot",
			},
			nil,
		),
		d3coldAllowed: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "pcie_slot", "d3cold_allowed"),
			"Whether the PCIe device supports D3cold power state (0/1).",
			[]string{
				"slot",
			},
			nil,
		),
		logger: logger,
	}, nil
}

func (c *pcieCollector) Update(ch chan<- prometheus.Metric) error {
	devices, err := filepath.Glob("/sys/bus/pci/devices/*")
	if err != nil {
		return fmt.Errorf("failed to list PCI devices: %w", err)
	}

	for _, devicePath := range devices {
		deviceID := filepath.Base(devicePath)
		if err := c.collectDeviceMetrics(ch, devicePath, deviceID); err != nil {
			c.logger.Debug("failed collecting metrics for device", "device", deviceID, "err", err)
			continue
		}
	}

	return nil
}

func (c *pcieCollector) collectDeviceMetrics(ch chan<- prometheus.Metric, devicePath, deviceID string) error {
	// Read IDs first
	vendorID := readFileContent(filepath.Join(devicePath, "vendor"))
	devID := readFileContent(filepath.Join(devicePath, "device"))
	subsysVendorID := readFileContent(filepath.Join(devicePath, "subsystem_vendor"))
	subsysDeviceID := readFileContent(filepath.Join(devicePath, "subsystem_device"))

	// Get human-readable names from pci.ids database
	vendor := getPCIVendorName(vendorID)
	device := getPCIDeviceName(vendorID, devID)
	subsysVendor := getPCIVendorName(subsysVendorID)
	subsysDevice := getPCISubsystemName(vendorID, devID, subsysVendorID, subsysDeviceID)

	// Parse class ID and convert to string
	classIDStr := readFileContent(filepath.Join(devicePath, "class"))
	classID := classIDStr
	classString := getPCIClassName(classIDStr)

	// Static info metric (constant value 1)
	infoLabels := []string{
		deviceID,
		vendorID,
		vendor,
		devID,
		device,
		subsysVendorID,
		subsysVendor,
		subsysDeviceID,
		subsysDevice,
		classID,
		classString,
		readFileContent(filepath.Join(devicePath, "revision")),
	}

	ch <- prometheus.MustNewConstMetric(
		c.info,
		prometheus.GaugeValue,
		1,
		infoLabels...,
	)

	// Current speed metric
	if currentSpeedStr := readFileContent(filepath.Join(devicePath, "current_link_speed")); currentSpeedStr != "unknown" {
		if currentSpeed, err := parseSpeed(currentSpeedStr); err == nil {
			ch <- prometheus.MustNewConstMetric(
				c.currentSpeed,
				prometheus.GaugeValue,
				currentSpeed,
				deviceID,
			)
		} else {
			c.logger.Debug("failed to parse current speed", "device", deviceID, "speed", currentSpeedStr, "error", err)
		}
	}

	// Current width metric
	if currentWidthStr := readFileContent(filepath.Join(devicePath, "current_link_width")); currentWidthStr != "unknown" {
		if currentWidth, err := parseWidth(currentWidthStr); err == nil {
			ch <- prometheus.MustNewConstMetric(
				c.currentWidth,
				prometheus.GaugeValue,
				currentWidth,
				deviceID,
			)
		} else {
			c.logger.Debug("failed to parse current width", "device", deviceID, "width", currentWidthStr, "error", err)
		}
	}

	// Max speed metric
	if maxSpeedStr := readFileContent(filepath.Join(devicePath, "max_link_speed")); maxSpeedStr != "unknown" {
		if maxSpeed, err := parseSpeed(maxSpeedStr); err == nil {
			ch <- prometheus.MustNewConstMetric(
				c.maxSpeed,
				prometheus.GaugeValue,
				maxSpeed,
				deviceID,
			)
		} else {
			c.logger.Debug("failed to parse max speed", "device", deviceID, "speed", maxSpeedStr, "error", err)
		}
	}

	// Max width metric
	if maxWidthStr := readFileContent(filepath.Join(devicePath, "max_link_width")); maxWidthStr != "unknown" {
		if maxWidth, err := parseWidth(maxWidthStr); err == nil {
			ch <- prometheus.MustNewConstMetric(
				c.maxWidth,
				prometheus.GaugeValue,
				maxWidth,
				deviceID,
			)
		} else {
			c.logger.Debug("failed to parse max width", "device", deviceID, "width", maxWidthStr, "error", err)
		}
	}

	// Power state metric
	if powerStateStr := readFileContent(filepath.Join(devicePath, "power_state")); powerStateStr != "unknown" {
		if powerState, err := parsePowerState(powerStateStr); err == nil {
			ch <- prometheus.MustNewConstMetric(
				c.powerState,
				prometheus.GaugeValue,
				powerState,
				deviceID,
			)
		} else {
			c.logger.Debug("failed to parse power state", "device", deviceID, "value", powerStateStr, "error", err)
		}
	}

	// D3cold allowed metric
	if d3coldAllowedStr := readFileContent(filepath.Join(devicePath, "d3cold_allowed")); d3coldAllowedStr != "unknown" {
		if d3coldAllowed, err := strconv.ParseFloat(d3coldAllowedStr, 64); err == nil {
			ch <- prometheus.MustNewConstMetric(
				c.d3coldAllowed,
				prometheus.GaugeValue,
				d3coldAllowed,
				deviceID,
			)
		} else {
			c.logger.Debug("failed to parse d3cold_allowed", "device", deviceID, "value", d3coldAllowedStr, "error", err)
		}
	}

	return nil
}

func readFileContent(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(content))
}

func loadPCIIds() {
	var file *os.File
	var err error

	// Try each possible path
	for _, path := range pciIdsPaths {
		file, err = os.Open(path)
		if err == nil {
			break
		}
	}
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var currentVendor, currentDevice, currentBaseClass, currentSubclass string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Handle class lines (starts with 'C')
		if strings.HasPrefix(line, "C") {
			parts := strings.SplitN(line, "  ", 2)
			if len(parts) >= 2 {
				classID := strings.TrimSpace(parts[0][1:]) // Remove 'C' prefix
				className := strings.TrimSpace(parts[1])
				pciClasses[classID] = className
				currentBaseClass = classID
				currentSubclass = ""
			}
			continue
		}

		// Handle subclass lines (single tab after class)
		if strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "\t\t") {
			line = strings.TrimPrefix(line, "\t")
			parts := strings.SplitN(line, "  ", 2)
			if len(parts) >= 2 && currentBaseClass != "" {
				subclassID := strings.TrimSpace(parts[0])
				subclassName := strings.TrimSpace(parts[1])
				// Store as base class + subclass (e.g., "0100" for SCSI storage controller)
				fullClassID := currentBaseClass + subclassID
				pciSubclasses[fullClassID] = subclassName
				currentSubclass = fullClassID
			}
			continue
		}

		// Handle programming interface lines (double tab after subclass)
		// We'll skip these for now as they're too specific and not commonly used in metrics
		if strings.HasPrefix(line, "\t\t") && !strings.HasPrefix(line, "\t\t\t") {
			continue
		}

		// Handle vendor lines (no leading whitespace, not starting with 'C')
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "C") {
			parts := strings.SplitN(line, "  ", 2)
			if len(parts) >= 2 {
				currentVendor = strings.TrimSpace(parts[0])
				pciVendors[currentVendor] = strings.TrimSpace(parts[1])
				currentDevice = ""
			}
			continue
		}

		// Handle device lines (single tab)
		if strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "\t\t") {
			line = strings.TrimPrefix(line, "\t")
			parts := strings.SplitN(line, "  ", 2)
			if len(parts) >= 2 && currentVendor != "" {
				currentDevice = strings.TrimSpace(parts[0])
				if pciDevices[currentVendor] == nil {
					pciDevices[currentVendor] = make(map[string]string)
				}
				pciDevices[currentVendor][currentDevice] = strings.TrimSpace(parts[1])
			}
			continue
		}

		// Handle subsystem lines (double tab)
		if strings.HasPrefix(line, "\t\t") {
			line = strings.TrimPrefix(line, "\t\t")
			parts := strings.SplitN(line, "  ", 2)
			if len(parts) >= 2 && currentVendor != "" && currentDevice != "" {
				subsysID := strings.TrimSpace(parts[0])
				subsysName := strings.TrimSpace(parts[1])
				key := fmt.Sprintf("%s:%s", currentVendor, currentDevice)
				if pciSubsystems[key] == nil {
					pciSubsystems[key] = make(map[string]string)
				}
				pciSubsystems[key][subsysID] = subsysName
			}
		}
	}
}

func getPCIVendorName(vendorID string) string {
	// Remove "0x" prefix if present
	vendorID = strings.TrimPrefix(vendorID, "0x")
	vendorID = strings.ToLower(vendorID)

	if name, ok := pciVendors[vendorID]; ok {
		return name
	}
	return vendorID // Return ID if name not found
}

func getPCIDeviceName(vendorID, deviceID string) string {
	// Remove "0x" prefix if present
	vendorID = strings.TrimPrefix(vendorID, "0x")
	deviceID = strings.TrimPrefix(deviceID, "0x")
	vendorID = strings.ToLower(vendorID)
	deviceID = strings.ToLower(deviceID)

	if devices, ok := pciDevices[vendorID]; ok {
		if name, ok := devices[deviceID]; ok {
			return name
		}
	}
	return deviceID // Return ID if name not found
}

func getPCISubsystemName(vendorID, deviceID, subsysVendorID, subsysDeviceID string) string {
	// Normalize all IDs
	vendorID = strings.TrimPrefix(vendorID, "0x")
	deviceID = strings.TrimPrefix(deviceID, "0x")
	subsysVendorID = strings.TrimPrefix(subsysVendorID, "0x")
	subsysDeviceID = strings.TrimPrefix(subsysDeviceID, "0x")

	key := fmt.Sprintf("%s:%s", vendorID, deviceID)
	subsysKey := fmt.Sprintf("%s:%s", subsysVendorID, subsysDeviceID)

	if subsystems, ok := pciSubsystems[key]; ok {
		if name, ok := subsystems[subsysKey]; ok {
			return name
		}
	}
	return subsysDeviceID
}

// getPCIClassName converts PCI class ID to human-readable string using pci.ids
func getPCIClassName(classID string) string {
	// Remove "0x" prefix if present and normalize
	classID = strings.TrimPrefix(classID, "0x")
	classID = strings.ToLower(classID)

	// Try to find the subclass first (4 digits: base class + subclass)
	if len(classID) >= 4 {
		if className, exists := pciSubclasses[classID]; exists {
			return className
		}
	}

	// If not found, try with just the base class (first 2 digits)
	if len(classID) >= 2 {
		baseClass := classID[:2]
		if className, exists := pciClasses[baseClass]; exists {
			return className
		}
	}

	// Return the original class ID if not found
	return "Unknown class (" + classID + ")"
}

// parseSpeed converts PCIe speed string to numeric GT/s value
func parseSpeed(speedStr string) (float64, error) {
	// Remove "GT/s PCIe" suffix if present
	speedStr = strings.TrimSuffix(speedStr, " GT/s PCIe")
	// Also handle case where only "GT/s" is present
	speedStr = strings.TrimSuffix(speedStr, " GT/s")
	speedStr = strings.TrimSpace(speedStr)

	return strconv.ParseFloat(speedStr, 64)
}

// parseWidth converts PCIe width string to numeric lanes value
func parseWidth(widthStr string) (float64, error) {
	// Remove "x" prefix if present
	widthStr = strings.TrimPrefix(widthStr, "x")
	widthStr = strings.TrimSpace(widthStr)

	return strconv.ParseFloat(widthStr, 64)
}

// parsePowerState converts PCIe power state string to numeric value
func parsePowerState(powerStateStr string) (float64, error) {
	// Remove any whitespace
	powerStateStr = strings.TrimSpace(powerStateStr)

	// Try to parse as numeric first (most common case)
	if powerState, err := strconv.ParseFloat(powerStateStr, 64); err == nil {
		return powerState, nil
	}

	// If numeric parsing fails, try to map string values
	switch strings.ToLower(powerStateStr) {
	case "d0", "0":
		return 0, nil
	case "d1", "1":
		return 1, nil
	case "d2", "2":
		return 2, nil
	case "d3hot", "3":
		return 3, nil
	case "d3cold", "4":
		return 4, nil
	default:
		return 0, fmt.Errorf("unknown power state: %s", powerStateStr)
	}
}
