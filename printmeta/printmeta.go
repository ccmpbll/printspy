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

type Info struct {
	LayerHeightMM  float64
	FillDensity    string
	PrinterModel   string
	Material       string // resolved filament_type for the tool actually used
	ToolIndex      int    // 0 for non-MMU
	FilamentUsedMM float64
	FilamentUsedG  float64
	FilamentCost   float64
	EstimatedSecs  int // normal-mode estimate
	MaxLayerZ      float64
	ObjectNames    []string
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

	usedMM := splitValues(kv["filament used [mm]"])
	usedG := splitValues(kv["filament used [g]"])
	cost := splitValues(kv["filament cost"])
	material := splitValues(kv["filament_type"])

	// Only a multi-tool printer's fields are actually arrays - a
	// single-extruder file has plain scalars and ToolIndex stays 0. Resolve
	// which index was used by whichever slot has non-zero filament used.
	idx := 0
	for i, v := range usedMM {
		if f, _ := strconv.ParseFloat(v, 64); f > 0 {
			idx = i
			break
		}
	}
	info.ToolIndex = idx

	info.FilamentUsedMM, _ = strconv.ParseFloat(at(usedMM, idx), 64)
	info.FilamentUsedG, _ = strconv.ParseFloat(at(usedG, idx), 64)
	info.FilamentCost, _ = strconv.ParseFloat(at(cost, idx), 64)
	info.Material = at(material, idx)

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
