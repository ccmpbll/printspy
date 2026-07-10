// Package printmeta extracts the print metadata (material, filament used,
// layer height, estimated time, etc) that PrusaLink's own HTTP API never
// exposes but that both .gcode and .bgcode files carry themselves - PrintSpy
// downloads the file directly and parses it once, at print completion.
package printmeta

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// ToolUsage is one tool's contribution to a print - a print using two
// colors/materials produces two of these, not one.
type ToolUsage struct {
	ToolIndex     int     `json:"tool_index"`
	Material      string  `json:"material"`
	FilamentUsedG float64 `json:"filament_used_g"`
	FilamentCost  float64 `json:"filament_cost"`
}

type Info struct {
	LayerHeightMM  float64
	FillDensity    string
	PrinterModel   string
	Material       string // primary (first) tool's material - back-compat, == Tools[0].Material
	ToolIndex      int    // primary (first) tool's index - back-compat, == Tools[0].ToolIndex
	FilamentUsedMM float64
	FilamentUsedG  float64 // print total (from the file's own "total filament used [g]" when present)
	FilamentCost   float64 // print total (from the file's own "total filament cost" when present)
	EstimatedSecs  int     // normal-mode estimate
	MaxLayerZ      float64
	ObjectNames    []string
	Tools          []ToolUsage // every tool with non-zero usage; len 1 for single-tool prints
	ToolChanges    int         // "total toolchanges"; 0 when absent (single-material prints)
}

func Parse(filename string, data []byte) (*Info, error) {
	if strings.HasSuffix(strings.ToLower(filename), ".bgcode") {
		return parseBgcode(data)
	}
	return parseGcode(data), nil
}

// .bgcode - binary block walker per prusa3d/libbgcode's spec.

const (
	blockFileMetadata    = 0
	blockGCode           = 1
	blockPrinterMetadata = 3
	blockPrintMetadata   = 4
	blockThumbnail       = 5
)

func parseBgcode(data []byte) (*Info, error) {
	if len(data) < 10 || string(data[0:4]) != "GCDE" {
		return nil, fmt.Errorf("not a bgcode file (bad magic)")
	}
	checksumType := binary.LittleEndian.Uint16(data[8:10])
	checksumSize := 0
	if checksumType != 0 {
		checksumSize = 4 // only CRC32 is defined
	}

	kv := map[string]string{}
	off := 10
	for off+8 <= len(data) {
		blockType := binary.LittleEndian.Uint16(data[off : off+2])
		compression := binary.LittleEndian.Uint16(data[off+2 : off+4])
		uncompressedSize := binary.LittleEndian.Uint32(data[off+4 : off+8])
		headerSize := 8
		dataSize := uint64(uncompressedSize)
		if compression != 0 {
			if off+12 > len(data) {
				break
			}
			dataSize = uint64(binary.LittleEndian.Uint32(data[off+8 : off+12]))
			headerSize = 12
		}
		off += headerSize

		var paramsSize int
		switch blockType {
		case blockThumbnail:
			paramsSize = 6 // Format(2)+Width(2)+Height(2)
		default:
			paramsSize = 2 // Encoding(2)
		}

		blockEnd := uint64(off) + uint64(paramsSize) + dataSize
		if blockEnd > uint64(len(data)) {
			break // truncated (e.g. a Range request that cut off mid-block)
		}

		if blockType == blockFileMetadata || blockType == blockPrinterMetadata || blockType == blockPrintMetadata {
			blockData := data[off+paramsSize : int(blockEnd)]
			if text, err := decodeText(blockData, compression); err == nil {
				parseINILines(text, kv)
			}
		}

		off = int(blockEnd) + checksumSize

		// Metadata blocks always precede the gcode body in the file's fixed
		// block ordering - nothing past this point is relevant to us, and
		// the gcode body itself can be several MB.
		if blockType == blockGCode {
			break
		}
	}

	return infoFromKV(kv), nil
}

func decodeText(b []byte, compression uint16) (string, error) {
	switch compression {
	case 0:
		return string(b), nil
	case 1:
		r, err := zlib.NewReader(bytes.NewReader(b))
		if err != nil {
			return "", err
		}
		defer r.Close()
		out, err := io.ReadAll(r)
		if err != nil {
			return "", err
		}
		return string(out), nil
	default:
		// Heatshrink (2, 3) is only ever used for gcode blocks in practice -
		// metadata blocks we care about are always 0 or 1.
		return "", fmt.Errorf("unsupported metadata block compression %d", compression)
	}
}

func parseINILines(text string, kv map[string]string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
}

// .gcode - plain text, metadata lives in "; key = value" footer comments.

var gcodeCommentRe = regexp.MustCompile(`^;\s*([^=]+?)\s*=\s*(.*)$`)

func parseGcode(data []byte) *Info {
	kv := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, ";") {
			continue
		}
		m := gcodeCommentRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		kv[strings.TrimSpace(m[1])] = strings.TrimSpace(m[2])
	}
	return infoFromKV(kv)
}

// Shared key -> Info resolution for both formats.

func infoFromKV(kv map[string]string) *Info {
	info := &Info{
		PrinterModel: kv["printer_model"],
		FillDensity:  kv["fill_density"],
	}
	info.LayerHeightMM, _ = strconv.ParseFloat(kv["layer_height"], 64)
	info.MaxLayerZ, _ = strconv.ParseFloat(kv["max_layer_z"], 64)
	info.EstimatedSecs = parseDuration(kv["estimated printing time (normal mode)"])
	info.ObjectNames = parseObjectNames(kv["objects_info"])

	info.ToolChanges, _ = strconv.Atoi(kv["total toolchanges"])

	usedMM := splitValues(kv["filament used [mm]"])
	usedG := splitValues(kv["filament used [g]"])
	cost := splitValues(kv["filament cost"])
	material := splitValues(kv["filament_type"])

	// Only a multi-tool printer's fields are actually arrays - a
	// single-extruder file has plain scalars. Every slot with non-zero
	// usage was genuinely printed with - an MMU job can and does use more
	// than one, not just the first.
	for i, v := range usedMM {
		if f, _ := strconv.ParseFloat(v, 64); f > 0 {
			g, _ := strconv.ParseFloat(at(usedG, i), 64)
			c, _ := strconv.ParseFloat(at(cost, i), 64)
			info.Tools = append(info.Tools, ToolUsage{
				ToolIndex: i, Material: at(material, i),
				FilamentUsedG: g, FilamentCost: c,
			})
		}
	}
	if len(info.Tools) == 0 {
		// Nothing measured non-zero - fall back to slot 0.
		info.Tools = append(info.Tools, ToolUsage{Material: at(material, 0)})
	}

	primary := info.Tools[0]
	info.ToolIndex = primary.ToolIndex
	info.Material = primary.Material
	info.FilamentUsedMM, _ = strconv.ParseFloat(at(usedMM, primary.ToolIndex), 64)

	// Prefer the slicer's own precomputed total over re-summing Tools - it
	// can include wipe-tower waste the per-tool breakdown doesn't
	// attribute to any one slot.
	if v, ok := kv["total filament used [g]"]; ok {
		info.FilamentUsedG, _ = strconv.ParseFloat(v, 64)
	} else {
		for _, t := range info.Tools {
			info.FilamentUsedG += t.FilamentUsedG
		}
	}
	if v, ok := kv["total filament cost"]; ok {
		info.FilamentCost, _ = strconv.ParseFloat(v, 64)
	} else {
		for _, t := range info.Tools {
			info.FilamentCost += t.FilamentCost
		}
	}

	return info
}

func at(vals []string, idx int) string {
	if idx < len(vals) {
		return vals[idx]
	}
	if len(vals) > 0 {
		return vals[0]
	}
	return ""
}

func splitValues(v string) []string {
	if v == "" {
		return nil
	}
	var parts []string
	switch {
	case strings.Contains(v, ";"):
		parts = strings.Split(v, ";")
	case strings.Contains(v, ","):
		parts = strings.Split(v, ",")
	default:
		return []string{v}
	}
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

var durationRe = regexp.MustCompile(`(?:(\d+)h)?\s*(?:(\d+)m)?\s*(?:(\d+)s)?`)

func parseDuration(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	m := durationRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	h, _ := strconv.Atoi(m[1])
	mm, _ := strconv.Atoi(m[2])
	ss, _ := strconv.Atoi(m[3])
	return h*3600 + mm*60 + ss
}

func parseObjectNames(s string) []string {
	if s == "" {
		return nil
	}
	var parsed struct {
		Objects []struct {
			Name string `json:"name"`
		} `json:"objects"`
	}
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil
	}
	names := make([]string, 0, len(parsed.Objects))
	for _, o := range parsed.Objects {
		names = append(names, o.Name)
	}
	return names
}
